// Package telemetry wires OpenTelemetry traces and metrics, trace-correlated slog logs,
// and CloudWatch Embedded Metric Format output used by alarms.
//
// Exporter selection is environment driven (standard OTel variables):
//   - OTEL_EXPORTER_OTLP_ENDPOINT (or the *_TRACES_/*_METRICS_ variants) set: export OTLP/HTTP.
//     In Lambda this points at the ADOT collector extension on http://localhost:4318.
//   - OTEL_TRACES_EXPORTER=console: pretty-print spans to stderr (local development).
//   - otherwise: spans are still created (so logs carry trace ids) but are not exported.
package telemetry

import (
	"context"
	"errors"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Providers owns the SDK providers so callers can flush before Lambda freezes.
type Providers struct {
	TP *sdktrace.TracerProvider
	MP *sdkmetric.MeterProvider
}

func otlpConfigured() bool {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// Init builds the providers and registers them globally.
func Init(ctx context.Context, serviceName, version string) (*Providers, error) {
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", version),
		),
	)
	if err != nil {
		return nil, err
	}

	tpOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res)}
	mpOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}

	switch {
	case otlpConfigured():
		te, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		// Lambda can freeze at any moment, so spans are batched but flushed explicitly per invoke.
		tpOpts = append(tpOpts, sdktrace.WithBatcher(te))
		me, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, err
		}
		mpOpts = append(mpOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me)))
	case os.Getenv("OTEL_TRACES_EXPORTER") == "console":
		te, err := stdouttrace.New(stdouttrace.WithPrettyPrint(), stdouttrace.WithWriter(os.Stderr))
		if err != nil {
			return nil, err
		}
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(te)))
	}

	p := &Providers{TP: sdktrace.NewTracerProvider(tpOpts...), MP: sdkmetric.NewMeterProvider(mpOpts...)}
	otel.SetTracerProvider(p.TP)
	otel.SetMeterProvider(p.MP)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return p, nil
}

// Flush pushes buffered telemetry out; call at the end of every Lambda invocation.
func (p *Providers) Flush(ctx context.Context) error {
	return errors.Join(p.TP.ForceFlush(ctx), p.MP.ForceFlush(ctx))
}

func (p *Providers) Shutdown(ctx context.Context) error {
	return errors.Join(p.TP.Shutdown(ctx), p.MP.Shutdown(ctx))
}
