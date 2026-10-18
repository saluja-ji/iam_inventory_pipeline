# Observability

## Signals

| Signal | How | Where it goes |
| --- | --- | --- |
| Traces | OpenTelemetry SDK, root span `pipeline.run`, child span per stage, `otelaws` span per AWS API call | OTLP/HTTP to the ADOT collector layer, then X-Ray (or any OTLP backend) |
| Metrics (OTel) | gauges `iam.roles`, `iam.users`, `iam.groups`, `iam.policies`, `iam.findings`, `iam.findings.critical_or_high`, `iam.changes`; histogram `pipeline.run.duration`; counter `pipeline.runs{status}` | OTLP, then CloudWatch namespace `IAMInventory/OTel` via `awsemf` |
| Metrics (EMF) | one JSON line per run: `run_success`, `roles_collected`, `policies_collected`, `findings_count`, `critical_high_findings`, `changes_count` | stdout, CloudWatch Logs extracts them into namespace `IAMInventory` |
| Logs | `slog` JSON with `run_id`, `dt`, counts, `duration_ms`, plus `trace_id` and `span_id` | CloudWatch Logs |

Logs and traces join on `trace_id`: copy it from a log line into the X-Ray trace search.

## Why metrics are emitted twice

The alarms must work even when the collector layer is missing or broken. EMF needs nothing but stdout and no `PutMetricData` permission, so alarms depend only on it. OpenTelemetry metrics are the portable, backend-neutral path and carry richer attributes. If you want just one, delete the EMF writer in `cmd/lambda/main.go` and point the alarms at the `IAMInventory/OTel` metrics.

## Configuration (standard OTel environment variables)

| Variable | Effect |
| --- | --- |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | enables OTLP export; set to `http://localhost:4318` by Terraform when the layer is configured |
| `OTEL_TRACES_EXPORTER=console` | print spans to stderr (used by `make local-demo`) |
| `OTEL_SERVICE_NAME` | resource service name (default `iam-inventory`) |
| neither set | spans are still created (logs keep trace ids) but nothing is exported |

Lambda freezes immediately after the handler returns, so the handler calls `ForceFlush` on both providers before returning. A flush failure logs a warning and never fails the run.

## Alarms

| Alarm | Condition | Why |
| --- | --- | --- |
| `...-lambda-errors` | `Errors > 0` over 5 min | the run failed |
| `...-no-successful-run` | `run_success` sum < 1 for 26 consecutive hourly periods, missing data = breaching | the schedule did not fire, or runs keep failing silently |
| `...-dlq-not-empty` | DLQ visible messages > 0 | an invocation exhausted its retries |

A dashboard (`iam-inventory-dev`) shows inventory size, findings, changes and run health.

## Useful queries

CloudWatch Logs Insights, last failures:

```
fields @timestamp, run_id, trace_id, error
| filter msg = "run failed"
| sort @timestamp desc
```
