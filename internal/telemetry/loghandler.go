package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceHandler decorates a slog.Handler with trace_id/span_id of the active span,
// so a log line in CloudWatch can be joined to its trace in X-Ray or any OTel backend.
type TraceHandler struct{ slog.Handler }

func NewTraceHandler(h slog.Handler) TraceHandler { return TraceHandler{h} }

func (h TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return h.Handler.Handle(ctx, r)
}

func (h TraceHandler) WithAttrs(a []slog.Attr) slog.Handler {
	return TraceHandler{h.Handler.WithAttrs(a)}
}
func (h TraceHandler) WithGroup(n string) slog.Handler { return TraceHandler{h.Handler.WithGroup(n)} }
