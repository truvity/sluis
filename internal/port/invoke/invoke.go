// Package invoke is the Trigger of a deployment that runs as AWS Lambda
// functions: "this target has work" is an asynchronous invoke of the function
// that runs the target's pass, with the payload {"kind":"run","target":"<id>"}
// (docs/integrations/aws-lambda.md).
//
// There is no process to deliver a notification to on Lambda, so Subscribe is
// unused and does nothing: the function is started by the platform, by this
// invoke or by its EventBridge schedule, and runs one pass under the target's
// lease. A notification is a hint that may be duplicated or lost, as the port
// says, and the lease and the schedule make both harmless.
package invoke

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/lambda/types"

	"github.com/truvity/sluis/internal/port"
)

// KindRun is the `kind` of the event this adapter sends: run one pass of the
// target now. The schedule's own is "tick"; both run the same pass.
const KindRun = "run"

// Event is the payload of an invocation of a controller function: what the
// schedule sends (kind "tick") and what this adapter sends (kind "run").
type Event struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// Config names the functions a notification invokes: one runs the GitHub
// controller's passes and one the Slack controller's.
type Config struct {
	// GitHub and Slack are each a function's name or ARN. A kind that has no
	// function is never invoked.
	GitHub string `json:"github,omitempty"`
	Slack  string `json:"slack,omitempty"`
	// Region overrides the platform's.
	Region string `json:"region,omitempty"`
	// Endpoint overrides the service's address, for a test against LocalStack.
	Endpoint string `json:"endpoint,omitempty"`
}

// The kinds of target, and so of function.
const (
	KindGitHub = "github"
	KindSlack  = "slack"
)

// API is the part of the Lambda client the adapter uses.
type API interface {
	Invoke(ctx context.Context, in *awslambda.InvokeInput, opts ...func(*awslambda.Options)) (*awslambda.InvokeOutput, error)
}

// Trigger invokes the function of a target's kind per notification.
type Trigger struct {
	api       API
	functions map[string]string

	mu   sync.RWMutex
	kind func(target string) string
}

var _ port.Trigger = (*Trigger)(nil)

// Open connects with the platform's credentials.
func Open(ctx context.Context, cfg Config) (*Trigger, error) {
	if cfg.GitHub == "" && cfg.Slack == "" {
		return nil, errors.New("invoke: no function: set github, slack or both")
	}
	var loaders []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		loaders = append(loaders, awsconfig.WithRegion(cfg.Region))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loaders...)
	if err != nil {
		return nil, fmt.Errorf("invoke: %w", err)
	}
	return New(awslambda.NewFromConfig(awsCfg, func(o *awslambda.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}), cfg)
}

// New builds the trigger over a client the caller made.
func New(api API, cfg Config) (*Trigger, error) {
	if api == nil || (cfg.GitHub == "" && cfg.Slack == "") {
		return nil, errors.New("invoke: a client and a function are required")
	}
	t := &Trigger{api: api, functions: map[string]string{}}
	if cfg.GitHub != "" {
		t.functions[KindGitHub] = cfg.GitHub
	}
	if cfg.Slack != "" {
		t.functions[KindSlack] = cfg.Slack
	}
	return t, nil
}

// SetKind tells the trigger which kind a target is, so that only that kind's
// function is invoked: a GitHub organisation's login and a Slack workspace's
// key are not told apart by their spelling, but by the policy that declares
// them. An empty answer is a target nobody declares. Until it is set, or for a
// target it does not know, every configured function is invoked, and one that
// does not know the target ends cleanly.
func (t *Trigger) SetKind(kind func(target string) string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.kind = kind
}

// Notify implements [port.Trigger]: an asynchronous invoke. The platform queues
// the event and runs it, so the call returns as soon as the event is accepted,
// which is what a console request wants.
func (t *Trigger) Notify(ctx context.Context, target string) error {
	payload, err := json.Marshal(Event{Kind: KindRun, Target: target})
	if err != nil {
		return err
	}
	t.mu.RLock()
	kind := t.kind
	t.mu.RUnlock()
	var names []string
	if kind != nil {
		if fn, ok := t.functions[kind(target)]; ok {
			names = []string{fn}
		}
	}
	if names == nil {
		for _, fn := range t.functions {
			names = append(names, fn)
		}
		slices.Sort(names)
	}
	var errs []error
	for _, fn := range names {
		errs = append(errs, t.invoke(ctx, fn, payload))
	}
	return errors.Join(errs...)
}

func (t *Trigger) invoke(ctx context.Context, function string, payload []byte) error {
	out, err := t.api.Invoke(ctx, &awslambda.InvokeInput{
		FunctionName:   aws.String(function),
		InvocationType: types.InvocationTypeEvent,
		Payload:        payload,
	})
	if err != nil {
		return fmt.Errorf("%w: invoke %s: %w", port.ErrUnavailable, function, err)
	}
	// An asynchronous invoke answers 202 when the event is queued.
	if out.StatusCode != 202 {
		return fmt.Errorf("%w: invoke %s answered %d, not 202", port.ErrUnavailable, function, out.StatusCode)
	}
	return nil
}

// Subscribe implements [port.Trigger]. Nothing is delivered to a Lambda
// function's process: the platform starts it. The returned stop does nothing.
func (t *Trigger) Subscribe(func(target string)) (stop func()) { return func() {} }

// The `invoke` adapter of the trigger concern (docs/design/ports.md). It works
// on Lambda, where there is no process to subscribe in.
func init() {
	port.Register(port.Descriptor{
		Name: "invoke", Concern: port.ConcernTrigger,
		Summary:  "\"Run a pass now\" as an asynchronous Lambda invoke of the controller function.",
		Requires: port.Requires{AWS: true},
		Runtimes: []port.Runtime{port.RuntimeLambda},
		Factory: func(ctx context.Context, s port.Settings) (any, error) {
			var cfg Config
			if err := s.Decode(&cfg); err != nil {
				return nil, err
			}
			return Open(ctx, cfg)
		},
	})
}
