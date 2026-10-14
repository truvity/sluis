// Package awsotel is how every AWS SDK client of sluis reports to
// OpenTelemetry: one function, called at every NewFromConfig site.
//
// The SDK takes its tracer and meter per client (Options.TracerProvider and
// Options.MeterProvider; aws.Config has no field for either), so a client
// built without [Providers] is silent. TestEveryAWSClientUsesAwsotel fails the
// build when a NewFromConfig call does not name this package.
//
// The adapters are the SDK's own, smithyoteltracing and smithyotelmetrics, over
// the process's global OpenTelemetry providers (the ones internal/telemetry
// installs). Where nothing is installed those are no-ops, so a client costs
// nothing when nobody collects.
package awsotel

import (
	"github.com/aws/smithy-go/metrics"
	"github.com/aws/smithy-go/metrics/smithyotelmetrics"
	"github.com/aws/smithy-go/tracing"
	"github.com/aws/smithy-go/tracing/smithyoteltracing"
	"go.opentelemetry.io/otel"
)

// Providers returns the tracer and meter provider a client takes:
//
//	svc.NewFromConfig(cfg, func(o *svc.Options) {
//		o.TracerProvider, o.MeterProvider = awsotel.Providers()
//	})
//
// The global providers are delegating, so a client built before telemetry
// starts still reports once it has.
func Providers() (tracing.TracerProvider, metrics.MeterProvider) {
	return smithyoteltracing.Adapt(otel.GetTracerProvider()),
		smithyotelmetrics.Adapt(otel.GetMeterProvider())
}
