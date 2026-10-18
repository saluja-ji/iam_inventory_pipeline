package telemetry

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RunStats is what one pipeline run reports.
type RunStats struct {
	Roles, Users, Groups, Policies int
	Findings, Critical, High       int
	Added, Removed, Modified       int
	Duration                       time.Duration
	Success                        bool
}

// Metrics records RunStats as OpenTelemetry instruments and, if EMF is set,
// as a CloudWatch Embedded Metric Format log line.
//
// Both paths exist on purpose: OTel metrics flow through the collector to any backend,
// while EMF keeps the CloudWatch alarms working even if the collector layer is absent.
type Metrics struct {
	EMF       io.Writer
	Namespace string
	Service   string

	roles, users, groups, policies, findings, critical metric.Int64Gauge
	changes                                            metric.Int64Gauge
	duration                                           metric.Float64Histogram
	runs                                               metric.Int64Counter
}

func NewMetrics(m metric.Meter, namespace, service string, emf io.Writer) (*Metrics, error) {
	x := &Metrics{EMF: emf, Namespace: namespace, Service: service}
	var err error
	g := func(name, desc string) metric.Int64Gauge {
		if err != nil {
			return nil
		}
		var gg metric.Int64Gauge
		gg, err = m.Int64Gauge(name, metric.WithDescription(desc), metric.WithUnit("{item}"))
		return gg
	}
	x.roles = g("iam.roles", "Roles in the latest snapshot")
	x.users = g("iam.users", "Users in the latest snapshot")
	x.groups = g("iam.groups", "Groups in the latest snapshot")
	x.policies = g("iam.policies", "Managed policies in the latest snapshot")
	x.findings = g("iam.findings", "Risk findings in the latest snapshot")
	x.critical = g("iam.findings.critical_or_high", "Critical and high findings")
	x.changes = g("iam.changes", "Identities changed since the previous snapshot")
	if err != nil {
		return nil, err
	}
	if x.duration, err = m.Float64Histogram("pipeline.run.duration", metric.WithUnit("s"), metric.WithDescription("Run duration")); err != nil {
		return nil, err
	}
	if x.runs, err = m.Int64Counter("pipeline.runs", metric.WithDescription("Pipeline runs by outcome")); err != nil {
		return nil, err
	}
	return x, nil
}

func (m *Metrics) Record(ctx context.Context, s RunStats) {
	status := "failure"
	if s.Success {
		status = "success"
		m.roles.Record(ctx, int64(s.Roles))
		m.users.Record(ctx, int64(s.Users))
		m.groups.Record(ctx, int64(s.Groups))
		m.policies.Record(ctx, int64(s.Policies))
		m.findings.Record(ctx, int64(s.Findings))
		m.critical.Record(ctx, int64(s.Critical+s.High))
		m.changes.Record(ctx, int64(s.Added+s.Removed+s.Modified), metric.WithAttributes(attribute.String("kind", "total")))
	}
	m.duration.Record(ctx, s.Duration.Seconds(), metric.WithAttributes(attribute.String("status", status)))
	m.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("status", status)))
	m.emitEMF(s)
}

// emitEMF writes one EMF document to stdout (Lambda ships it to CloudWatch Logs,
// which extracts the metrics for free, with no PutMetricData permission needed).
func (m *Metrics) emitEMF(s RunStats) {
	if m.EMF == nil {
		return
	}
	success := 0
	if s.Success {
		success = 1
	}
	vals := map[string]int{
		"run_success": success,
	}
	units := []map[string]string{{"Name": "run_success", "Unit": "Count"}}
	add := func(name string, v int) {
		vals[name] = v
		units = append(units, map[string]string{"Name": name, "Unit": "Count"})
	}
	if s.Success { // counts are meaningless on failure and would pollute graphs with zeros
		add("roles_collected", s.Roles)
		add("users_collected", s.Users)
		add("groups_collected", s.Groups)
		add("policies_collected", s.Policies)
		add("findings_count", s.Findings)
		add("critical_high_findings", s.Critical+s.High)
		add("changes_count", s.Added+s.Removed+s.Modified)
	}
	doc := map[string]any{
		"_aws": map[string]any{
			"Timestamp": time.Now().UnixMilli(),
			"CloudWatchMetrics": []any{map[string]any{
				"Namespace":  m.Namespace,
				"Dimensions": [][]string{{"Service"}},
				"Metrics":    units,
			}},
		},
		"Service":          m.Service,
		"duration_seconds": s.Duration.Seconds(),
	}
	for k, v := range vals {
		doc[k] = v
	}
	b, _ := json.Marshal(doc)
	_, _ = m.EMF.Write(append(b, '\n'))
}
