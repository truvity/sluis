package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v7"
)

// Token requests are tried a few times when the failure may pass.
//
// In a job every helm or kubectl call runs `sluisctl kube-token` again,
// so a long job makes many round trips to the job's token service and to
// the issuer; one dropped connection among them used to fail the step.
// A few quick attempts ride that out, and a request that cannot succeed —
// a refusal, a malformed answer — is still answered at once.
const requestTries = 4

// newRequestBackOff is the wait between tries: a quarter of a second,
// doubling to at most two, jittered so parallel jobs do not retry in step.
// A variable so a test can shorten the waits rather than sleep through them.
var newRequestBackOff = func() backoff.BackOff {
	return &backoff.ExponentialBackOff{
		InitialInterval:     250 * time.Millisecond,
		RandomizationFactor: 0.5,
		Multiplier:          2,
		MaxInterval:         2 * time.Second,
	}
}

// retry runs operation under the request policy. The operation marks a
// failure that is not worth another try with [backoff.Permanent]. A failure
// that outlasts the tries notes how many were made, and one cut short by
// ctx keeps the failure that preceded it, so its wrapping still decides
// the exit code.
func retry[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	tries := 0
	result, err := backoff.Retry(ctx, func() (T, error) {
		tries++
		return operation()
	}, backoff.WithBackOff(newRequestBackOff()), backoff.WithMaxTries(requestTries))
	if err == nil {
		return result, nil
	}
	err = retryFailure(err)
	if tries > 1 {
		err = fmt.Errorf("%w (after %d attempts)", err, tries)
	}
	return result, err
}

// retryFailure is the error a failed [backoff.Retry] stands for. backoff
// reports every failure as a [backoff.RetryError] that pairs the last
// attempt's error with why it stopped; the callers read the attempt's own
// error, which carries the wrapping that decides the exit code. Only a stop
// that came from the context adds its cause, unless the attempt's error
// already is that cause.
func retryFailure(err error) error {
	failure := backoff.AsRetryError(err)
	switch {
	case failure == nil:
		return err
	case errors.Is(failure.Cause, backoff.ErrPermanent),
		errors.Is(failure.Cause, backoff.ErrExhausted),
		errors.Is(failure.Cause, backoff.ErrMaxElapsedTime):
		return failure.LastErr
	case failure.LastErr == nil || errors.Is(failure.LastErr, failure.Cause):
		return failure.Cause
	default:
		return errors.Join(failure.LastErr, failure.Cause)
	}
}

// transientStatus is an answer that may be different a moment later: the
// server shedding load or failing on its side. Every other 4xx is final.
func transientStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= http.StatusInternalServerError
}

// retryableAnswer is the part of transientStatus that retryingTransport
// retries: a failure on the server's side. A 429 is not, because it is the
// issuer's or its edge's rate limit answering, and a retry is one more request
// against the very limit that refused: four tries per helper run turned one
// refusal into a storm that kept the limit tripped for every other caller. The
// 429 is returned as the answer it is, for the caller to back off from.
func retryableAnswer(code int) bool {
	return code >= http.StatusInternalServerError
}

// transientTransport says whether a request that got no answer at all is
// worth another try. A connection reset or refused, a timeout, a
// temporary DNS failure all are; a certificate the tool does not trust, a
// name that does not exist, or the caller giving up are not.
func transientTransport(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var (
		dns       *net.DNSError
		unknownCA x509.UnknownAuthorityError
		hostname  x509.HostnameError
		invalid   x509.CertificateInvalidError
		verify    *tls.CertificateVerificationError
	)
	switch {
	case errors.As(err, &dns):
		return dns.IsTemporary || dns.IsTimeout
	case errors.As(err, &unknownCA), errors.As(err, &hostname), errors.As(err, &invalid), errors.As(err, &verify):
		return false
	}
	return true
}

// transient returns err as it is when another attempt may help, and marked
// permanent when it cannot.
func transient(retry bool, err error) error {
	if retry {
		return err
	}
	return backoff.Permanent(err)
}

// retryingTransport applies the request policy to a client whose requests are
// built elsewhere — the token exchange, which the tokens library makes and
// which reports every non-200 alike, so the retry has to happen below it.
// A server error is retried and, when it outlasts the attempts, returned as the
// answer it is, for the caller to read as before. A 429 is never retried: it is
// returned at once (see [retryableAnswer]).
type retryingTransport struct {
	base http.RoundTripper
}

func (t retryingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	if request.Body != nil && request.GetBody != nil {
		// Every attempt sends a fresh copy; the original is this
		// transport's to close.
		defer func() { _ = request.Body.Close() }()
	}
	var last *http.Response
	_, err := retry(ctx, func() (struct{}, error) {
		if last != nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(last.Body, 1<<16))
			_ = last.Body.Close()
			last = nil
		}
		attempt := request.Clone(ctx)
		if request.Body != nil && request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return struct{}{}, backoff.Permanent(err)
			}
			attempt.Body = body
		}
		response, err := t.base.RoundTrip(attempt)
		if err != nil {
			// A body that cannot be replayed was spent by this attempt.
			if !transientTransport(ctx, err) || (request.Body != nil && request.GetBody == nil) {
				return struct{}{}, backoff.Permanent(err)
			}
			return struct{}{}, err
		}
		last = response
		if retryableAnswer(response.StatusCode) {
			return struct{}{}, fmt.Errorf("answered %s", response.Status)
		}
		return struct{}{}, nil
	})
	if last != nil && ctx.Err() == nil {
		return last, nil
	}
	if last != nil {
		_ = last.Body.Close()
	}
	return nil, err
}

// retryingClient is the HTTP client for token requests: retried, each
// attempt's answer bounded, and the whole request bounded too.
func retryingClient() *http.Client {
	return retryingClientTrusting(nil)
}

// retryingClientTrusting is retryingClient verifying servers against
// roots; nil is the system's, as for retryingClient.
func retryingClientTrusting(roots *x509.CertPool) *http.Client {
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.ResponseHeaderTimeout = 30 * time.Second
	if roots != nil {
		base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	return &http.Client{
		Transport: retryingTransport{base: base},
		Timeout:   2 * time.Minute,
	}
}
