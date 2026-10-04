// Package consoleauth is how a controller proves itself to the console.
//
// In a cluster it is the pod's projected ServiceAccount token, read afresh on
// every call. On Lambda there is none, and the proof is the function role's
// outbound web identity token (`sts:GetWebIdentityToken`), scoped to the one
// audience the console's issuer verifies AWS roles for, and cached until it is
// near its expiry so a pass that calls the console many times asks STS once.
//
// The issuer verifies the token against the account's published key set, with
// the same verifier token exchange uses, and the policy's `aws` matchers decide
// what the role may do (docs/integrations/aws-lambda.md).
package consoleauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Source yields the bearer a call to the console carries.
type Source interface {
	Token(ctx context.Context) (string, error)
}

// File reads a projected token afresh each time: the kubelet rotates it under
// the pod.
type File string

// Token implements [Source].
func (f File) Token(context.Context) (string, error) {
	raw, err := os.ReadFile(string(f))
	if err != nil {
		return "", fmt.Errorf("read this pod's ServiceAccount token: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

// STS is the part of the STS client the AWS source uses.
type STS interface {
	GetWebIdentityToken(ctx context.Context, in *sts.GetWebIdentityTokenInput, opts ...func(*sts.Options)) (*sts.GetWebIdentityTokenOutput, error)
}

const (
	// lifetime is how long a token is requested for: long enough that one pass
	// uses one, and well inside the 5 minutes to 1 hour AWS allows.
	lifetime = 10 * time.Minute
	// refreshBefore is how close to its expiry a cached token is replaced.
	refreshBefore = 2 * time.Minute
	// algorithm is one the issuer's AWS verifier accepts by default.
	algorithm = "ES384"
)

// AWS is the function role's web identity token, for one audience.
type AWS struct {
	api      STS
	audience string
	now      func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// NewAWS connects with the function's role. The audience must equal the one the
// issuer's AWS federation file names.
//
// The source is shared by audience for the life of the process: a function
// assembles its controller per invocation, and a warm execution environment
// should reuse the token, not ask STS again.
func NewAWS(ctx context.Context, audience string) (*AWS, error) {
	shared.Lock()
	defer shared.Unlock()
	if a, ok := shared.byAudience[audience]; ok {
		return a, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("consoleauth: %w", err)
	}
	a, err := NewAWSWith(sts.NewFromConfig(cfg), audience, time.Now)
	if err != nil {
		return nil, err
	}
	shared.byAudience[audience] = a
	return a, nil
}

var shared = struct {
	sync.Mutex
	byAudience map[string]*AWS
}{byAudience: map[string]*AWS{}}

// NewAWSWith is [NewAWS] over a client and a clock the caller made.
func NewAWSWith(api STS, audience string, now func() time.Time) (*AWS, error) {
	if strings.TrimSpace(audience) == "" || api == nil {
		return nil, errors.New("consoleauth: an STS client and an audience are required: an unscoped token would be a proof for anything")
	}
	return &AWS{api: api, audience: audience, now: now}, nil
}

// Token implements [Source]. The token is cached until it is within two minutes
// of its expiry.
func (a *AWS) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && a.expires.Sub(a.now()) > refreshBefore {
		return a.token, nil
	}
	out, err := a.api.GetWebIdentityToken(ctx, &sts.GetWebIdentityTokenInput{
		Audience:         []string{a.audience},
		SigningAlgorithm: aws.String(algorithm),
		DurationSeconds:  aws.Int32(int32(lifetime / time.Second)),
	})
	if err != nil {
		return "", fmt.Errorf("sts:GetWebIdentityToken: %w", err)
	}
	if aws.ToString(out.WebIdentityToken) == "" {
		return "", errors.New("sts:GetWebIdentityToken returned no token")
	}
	a.token = aws.ToString(out.WebIdentityToken)
	a.expires = a.now().Add(lifetime)
	if out.Expiration != nil {
		a.expires = *out.Expiration
	}
	return a.token, nil
}

// Interceptor presents the source's token as the bearer of every call.
func Interceptor(src Source) connect.Option { return connect.WithInterceptors(Unary(src)) }

// Unary is [Interceptor] as the interceptor itself.
func Unary(src Source) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			token, err := src.Token(ctx)
			if err != nil {
				return nil, err
			}
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}
