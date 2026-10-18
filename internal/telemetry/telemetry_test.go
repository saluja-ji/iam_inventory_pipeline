package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestEMFShape(t *testing.T) {
	var buf bytes.Buffer
	m, err := NewMetrics(noop.NewMeterProvider().Meter("t"), "IAMInventory", "iam-inventory", &buf)
	if err != nil {
		t.Fatal(err)
	}
	m.Record(context.Background(), RunStats{Roles: 3, Findings: 2, High: 1, Success: true, Duration: time.Second})

	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("EMF not one JSON doc: %v\n%s", err, buf.String())
	}
	if doc["roles_collected"].(float64) != 3 || doc["run_success"].(float64) != 1 || doc["Service"] != "iam-inventory" {
		t.Fatalf("EMF values wrong: %v", doc)
	}
	if _, ok := doc["_aws"].(map[string]any)["CloudWatchMetrics"]; !ok {
		t.Fatal("missing CloudWatchMetrics directive")
	}

	buf.Reset()
	m.Record(context.Background(), RunStats{Success: false})
	if strings.Contains(buf.String(), "roles_collected") || !strings.Contains(buf.String(), `"run_success":0`) {
		t.Fatalf("failure EMF wrong: %s", buf.String())
	}
}

func TestTraceHandlerAddsIDs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(NewTraceHandler(slog.NewJSONHandler(&buf, nil)))
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("t").Start(context.Background(), "s")
	log.InfoContext(ctx, "hello")
	span.End()
	if !strings.Contains(buf.String(), `"trace_id":"`+span.SpanContext().TraceID().String()) {
		t.Fatalf("trace id missing: %s", buf.String())
	}
	buf.Reset()
	log.Info("no span")
	if strings.Contains(buf.String(), "trace_id") {
		t.Fatal("trace_id must be absent without a span")
	}
}
