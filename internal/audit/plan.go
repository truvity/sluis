package audit

import (
	"fmt"
	"os"
	"time"

	"github.com/truvity/sluis/internal/port"
)

// OnLambda reports whether this process is an AWS Lambda function.
func OnLambda() bool { return os.Getenv("AWS_LAMBDA_FUNCTION_NAME") != "" }

// sqsSettings are the `adapters.audit.settings` of the `sqs` adapter.
type sqsSettings struct {
	QueueURL string `json:"queueURL"`
	Region   string `json:"region"`
	Endpoint string `json:"endpoint"`
	Timeout  string `json:"timeout"`
}

// FromPlan applies the audit concern of the resolved adapter table to cfg,
// which holds what the legacy `audit` keys said:
//
//   - `connect` is the legacy mapping, used as configured (`audit.writer`
//     and `audit.tokenFile`); it is refused without a writer.
//   - `log` keeps nothing beyond the log line, and ignores a writer.
//   - `sqs` publishes to the queue in the adapter's settings. There is no
//     receiver on that path, so the writer is cleared and the catalogue is not
//     registered: it is delivered in the audit writer's package. On Lambda the
//     trail sends each record before the call returns.
//
// An empty table (an adapter that does not plan) leaves cfg as it was.
func FromPlan(cfg Config, t port.Table) (Config, error) {
	ch, ok := t[port.ConcernAudit]
	if !ok {
		return cfg, nil
	}
	switch ch.Adapter {
	case "connect":
		if cfg.Writer == "" {
			return cfg, fmt.Errorf("adapters: audit adapter %q needs audit.writer", ch.Adapter)
		}
	case "log":
		cfg.Writer, cfg.SQS = "", nil
	case "sqs":
		var s sqsSettings
		if err := ch.Settings.Decode(&s); err != nil {
			return cfg, fmt.Errorf("adapters.audit.settings: %w", err)
		}
		if s.QueueURL == "" {
			return cfg, fmt.Errorf("adapters: audit adapter %q needs settings.queueURL", ch.Adapter)
		}
		c := &SQSConfig{QueueURL: s.QueueURL, Region: s.Region, Endpoint: s.Endpoint}
		if s.Timeout != "" {
			d, err := time.ParseDuration(s.Timeout)
			if err != nil || d <= 0 {
				return cfg, fmt.Errorf("adapters.audit.settings.timeout: %q is not a positive duration", s.Timeout)
			}
			c.Timeout = d
		}
		cfg.SQS, cfg.Writer = c, ""
		cfg.Sync = OnLambda()
	default:
		return cfg, fmt.Errorf("adapters: audit adapter %q is registered but this build does not serve with it yet", ch.Adapter)
	}
	return cfg, nil
}

// stubSQS is the client [Verify] builds the publisher over: it is never called.
type stubSQS struct{ SQSAPI }

// Verify builds what [Open] builds from cfg, as far as it can without reaching
// anything: the catalogue loads, and the `sqs` publisher is constructed from
// its settings (a missing queue is refused). A `connect` trail is not opened,
// because opening it registers the catalogue with the installation. Run it on
// the Config [FromPlan] returned.
func Verify(cfg Config) error {
	if _, _, err := Catalogue(); err != nil {
		return err
	}
	if cfg.SQS != nil {
		if _, err := newSQSPublisher(stubSQS{}, *cfg.SQS); err != nil {
			return err
		}
	}
	return nil
}
