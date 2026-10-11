// Package lambdatest is a container-free end-to-end harness for the Lambda
// functions: an httptest server that speaks the Lambda Invoke API and routes
// FunctionName plus Qualifier to in-process [lambdaapp.Function] handlers.
//
// The qualifier is what a deployment makes it: the alias `live-<class>` is the
// caller's identity (internal/modcall/lambdacall), so the emulator hands the
// handler an invoked-function ARN ending in the qualifier and the function
// derives the caller class from it exactly as it does on the platform. Nothing
// else about the caller reaches the function.
//
// For tests only. State is the in-memory port, secrets are a small SSM fake
// ([SSM]), and GitHub is internal/githubapp/githubfake; no container runs.
package lambdatest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-lambda-go/lambdacontext"
	"github.com/aws/aws-sdk-go-v2/aws"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"

	"github.com/truvity/sluis/internal/lambdaapp"
	"github.com/truvity/sluis/internal/modcall/lambdacall"
)

// Region and Account are what the emulator's ARNs carry.
const (
	Region  = "eu-central-1"
	Account = "000000000000"
)

// Call is one invocation the emulator served.
type Call struct {
	Function  string
	Qualifier string
	// Class is the caller class the qualifier names (`live-issuer` is "issuer");
	// empty for `live` and for a qualifier that is no class.
	Class   string
	Payload []byte
}

// Emulator is the Lambda Invoke API over httptest.
type Emulator struct {
	srv *httptest.Server

	mu    sync.Mutex
	fns   map[string]*lambdaapp.Function
	calls []Call
}

// New starts an emulator with no function registered.
func New(t testing.TB) *Emulator {
	t.Helper()
	e := &Emulator{fns: map[string]*lambdaapp.Function{}}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.srv.Close)
	return e
}

// Register makes fn the function name answers to. A name is the function's
// bare name; invoking by ARN reaches the same function.
func (e *Emulator) Register(name string, fn *lambdaapp.Function) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fns[name] = fn
}

// URL is the emulator's endpoint.
func (e *Emulator) URL() string { return e.srv.URL }

// Client is a Lambda client for the emulator: what the production transport
// ([lambdacall.Caller], the run-now trigger) is given as its API.
func (e *Emulator) Client() *awslambda.Client {
	return awslambda.New(awslambda.Options{
		Region:       Region,
		BaseEndpoint: aws.String(e.srv.URL),
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "example"}, nil
		}),
	})
}

// Calls is every invocation served so far, in order.
func (e *Emulator) Calls() []Call {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Call(nil), e.calls...)
}

// Result is what an invocation answered: the payload, and FunctionError when
// the function failed (the platform answers 200 with the error set).
type Result struct {
	Payload       []byte
	FunctionError string
}

// Invoke invokes function through qualifier with the event, over the wire.
func (e *Emulator) Invoke(ctx context.Context, function, qualifier string, event []byte) (Result, error) {
	in := &awslambda.InvokeInput{FunctionName: aws.String(function), Payload: event}
	if qualifier != "" {
		in.Qualifier = aws.String(qualifier)
	}
	out, err := e.Client().Invoke(ctx, in)
	if err != nil {
		return Result{}, err
	}
	return Result{Payload: out.Payload, FunctionError: aws.ToString(out.FunctionError)}, nil
}

// serve answers POST /2015-03-31/functions/{name}/invocations?Qualifier=.
func (e *Emulator) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/2015-03-31/functions/")
	name, tail, _ := strings.Cut(rest, "/")
	if !ok || tail != "invocations" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	name, qualifier := splitName(name, r.URL.Query().Get("Qualifier"))
	e.mu.Lock()
	fn := e.fns[name]
	e.mu.Unlock()
	if fn == nil {
		w.Header().Set("X-Amzn-Errortype", "ResourceNotFoundException")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"Type": "User", "Message": "Function not found: " + name})
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	e.mu.Lock()
	e.calls = append(e.calls, Call{Function: name, Qualifier: qualifier, Class: lambdacall.ClassOf(qualifier), Payload: body})
	e.mu.Unlock()

	switch r.Header.Get("X-Amz-Invocation-Type") {
	case "DryRun":
		w.WriteHeader(http.StatusNoContent)
		return
	}
	out, ferr := run(r.Context(), fn, name, qualifier, body)
	if r.Header.Get("X-Amz-Invocation-Type") == "Event" {
		// Asynchronous on the platform; run to completion here so a test reads
		// the effect without sleeping.
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if ferr != "" {
		w.Header().Set("X-Amz-Function-Error", "Unhandled")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// splitName takes a bare name, or an ARN that may carry the alias, and the
// Qualifier parameter, which wins when both are given.
func splitName(name, qualifier string) (string, string) {
	if i := strings.Index(name, ":function:"); i >= 0 {
		name = name[i+len(":function:"):]
	}
	name, alias, _ := strings.Cut(name, ":")
	if qualifier == "" {
		qualifier = alias
	}
	return name, qualifier
}

// run invokes the handler as the platform does: the context carries the
// invoked ARN, the qualifier last, and a panic is a function error.
func run(ctx context.Context, fn *lambdaapp.Function, name, qualifier string, event []byte) (out []byte, ferr string) {
	arn := fmt.Sprintf("arn:aws:lambda:%s:%s:function:%s", Region, Account, name)
	if qualifier != "" {
		arn += ":" + qualifier
	}
	ctx = lambdacontext.NewContext(ctx, &lambdacontext.LambdaContext{InvokedFunctionArn: arn, AwsRequestID: "emulated"})
	fail := func(msg string) ([]byte, string) {
		b, _ := json.Marshal(map[string]string{"errorMessage": msg, "errorType": "Error"})
		return b, msg
	}
	defer func() {
		if p := recover(); p != nil {
			out, ferr = fail(fmt.Sprint("panic: ", p))
		}
	}()
	res, err := fn.Handler.Handle(ctx, json.RawMessage(event))
	if fn.Flush != nil {
		fn.Flush(ctx)
	}
	if err != nil {
		return fail(err.Error())
	}
	b, err := json.Marshal(res)
	if err != nil {
		return fail(err.Error())
	}
	return b, ""
}
