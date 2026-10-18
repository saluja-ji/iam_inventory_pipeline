// Package pipeline orchestrates one run: collect, store raw, analyze, curate, diff, alert.
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/saluja-ji/iam-inventory/internal/analyze"
	"github.com/saluja-ji/iam-inventory/internal/collector"
	"github.com/saluja-ji/iam-inventory/internal/model"
	"github.com/saluja-ji/iam-inventory/internal/notify"
	"github.com/saluja-ji/iam-inventory/internal/store"
	"github.com/saluja-ji/iam-inventory/internal/telemetry"
	"github.com/saluja-ji/iam-inventory/internal/transform"
)

var tracer = otel.Tracer("iam-inventory/pipeline")

// IST is a fixed zone so partitions follow Indian dates without needing tzdata in the Lambda image.
var IST = time.FixedZone("IST", 5*3600+30*60)

// maxDiffLookbackDays bounds how far back we search for a previous snapshot after a missed day.
const maxDiffLookbackDays = 7

type Config struct {
	AlertMinSeverity analyze.Severity
	MaxKeyAgeDays    int
}

type Pipeline struct {
	Source   collector.Source
	Store    store.Backend
	Notifier notify.Notifier
	Metrics  *telemetry.Metrics
	Log      *slog.Logger
	Cfg      Config
	Now      func() time.Time
}

type Result struct {
	RunID    string `json:"run_id"`
	DT       string `json:"dt"`
	Roles    int    `json:"roles"`
	Users    int    `json:"users"`
	Groups   int    `json:"groups"`
	Policies int    `json:"policies"`
	Findings int    `json:"findings"`
	Changes  int    `json:"changes"`
	Alerted  bool   `json:"alerted"`
}

func newRunID(now time.Time) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b)
}

// Run executes the pipeline. Metrics are recorded for both success and failure.
func (p *Pipeline) Run(ctx context.Context) (res Result, err error) {
	now := p.Now()
	start := time.Now()
	runID := newRunID(now)
	dt := now.In(IST).Format("2006-01-02")
	res.RunID, res.DT = runID, dt

	ctx, span := tracer.Start(ctx, "pipeline.run", trace.WithAttributes(
		attribute.String("run.id", runID), attribute.String("run.dt", dt)))
	log := p.Log.With("run_id", runID, "dt", dt)
	stats := telemetry.RunStats{}

	defer func() {
		stats.Duration = time.Since(start)
		stats.Success = err == nil
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			log.ErrorContext(ctx, "run failed", "error", err, "duration_ms", stats.Duration.Milliseconds())
		} else {
			log.InfoContext(ctx, "run succeeded", "roles", res.Roles, "users", res.Users, "groups", res.Groups,
				"policies", res.Policies, "findings", res.Findings, "changes", res.Changes,
				"duration_ms", stats.Duration.Milliseconds())
		}
		span.End()
		if p.Metrics != nil {
			p.Metrics.Record(ctx, stats)
		}
	}()

	snap, err := step(ctx, "collect", func(ctx context.Context) (*model.Snapshot, error) {
		return p.Source.Collect(ctx, runID, now)
	})
	if err != nil {
		return res, err
	}
	res.Roles, res.Users, res.Groups, res.Policies = len(snap.Roles), len(snap.Users), len(snap.Groups), len(snap.Policies)
	stats.Roles, stats.Users, stats.Groups, stats.Policies = res.Roles, res.Users, res.Groups, res.Policies
	log.InfoContext(ctx, "collected", "roles", res.Roles, "users", res.Users, "groups", res.Groups, "policies", res.Policies)

	if _, err = step(ctx, "store.raw", func(ctx context.Context) (struct{}, error) {
		return struct{}{}, p.putJSON(ctx, store.RawKey(dt), snap)
	}); err != nil {
		return res, err
	}

	findings, _ := step(ctx, "analyze", func(context.Context) ([]analyze.Finding, error) {
		return analyze.Evaluate(snap, analyze.Options{MaxKeyAgeDays: p.Cfg.MaxKeyAgeDays}), nil
	})
	res.Findings, stats.Findings = len(findings), len(findings)
	for _, f := range findings {
		switch f.Severity {
		case analyze.Critical:
			stats.Critical++
		case analyze.High:
			stats.High++
		}
	}

	if _, err = step(ctx, "store.curated", func(ctx context.Context) (struct{}, error) {
		tables, err := transform.Build(snap, findings)
		if err != nil {
			return struct{}{}, err
		}
		for name, body := range tables {
			if err := p.Store.Put(ctx, store.CuratedKey(name, dt), body, "application/vnd.apache.parquet"); err != nil {
				return struct{}{}, err
			}
		}
		return struct{}{}, p.putJSON(ctx, store.FindingsKey(dt), findings)
	}); err != nil {
		return res, err
	}

	diff, err := step(ctx, "diff", func(ctx context.Context) (analyze.Diff, error) {
		prev, err := p.previous(ctx, dt)
		if err != nil {
			return analyze.Diff{}, err
		}
		d := analyze.Compare(prev, snap)
		return d, p.putJSON(ctx, store.DiffKey(dt), d)
	})
	if err != nil {
		return res, err
	}
	res.Changes = diff.Total()
	stats.Added = len(diff.Roles.Added) + len(diff.Users.Added) + len(diff.Groups.Added) + len(diff.Policies.Added)
	stats.Removed = len(diff.Roles.Removed) + len(diff.Users.Removed) + len(diff.Groups.Removed) + len(diff.Policies.Removed)
	stats.Modified = res.Changes - stats.Added - stats.Removed

	alertable := filterMin(findings, p.Cfg.AlertMinSeverity)
	if len(alertable) > 0 {
		_, err = step(ctx, "notify", func(ctx context.Context) (struct{}, error) {
			subject := fmt.Sprintf("[IAM Inventory] %d finding(s) at %s or above (%s)", len(alertable), p.Cfg.AlertMinSeverity, dt)
			return struct{}{}, p.Notifier.Notify(ctx, subject, summary(dt, runID, res, alertable, diff))
		})
		if err != nil {
			// The data is safely stored; a failed alert must still fail the run so the error alarm fires.
			return res, err
		}
		res.Alerted = true
	}
	return res, nil
}

// previous finds the most recent earlier raw snapshot, tolerating a few missed days.
func (p *Pipeline) previous(ctx context.Context, dt string) (*model.Snapshot, error) {
	day, err := time.Parse("2006-01-02", dt)
	if err != nil {
		return nil, err
	}
	for i := 1; i <= maxDiffLookbackDays; i++ {
		b, err := p.Store.Get(ctx, store.RawKey(day.AddDate(0, 0, -i).Format("2006-01-02")))
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var s model.Snapshot
		if err := json.Unmarshal(b, &s); err != nil {
			return nil, fmt.Errorf("decode previous snapshot: %w", err)
		}
		return &s, nil
	}
	return nil, nil
}

func (p *Pipeline) putJSON(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return p.Store.Put(ctx, key, b, "application/json")
}

// step wraps a unit of work in a child span.
func step[T any](ctx context.Context, name string, fn func(context.Context) (T, error)) (T, error) {
	ctx, span := tracer.Start(ctx, name)
	defer span.End()
	v, err := fn(ctx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	return v, err
}

func filterMin(in []analyze.Finding, min analyze.Severity) []analyze.Finding {
	var out []analyze.Finding
	for _, f := range in {
		if f.Severity.Rank() >= min.Rank() {
			out = append(out, f)
		}
	}
	return out
}

func summary(dt, runID string, r Result, fs []analyze.Finding, d analyze.Diff) string {
	var b strings.Builder
	fmt.Fprintf(&b, "IAM inventory for %s (run %s)\n\n", dt, runID)
	fmt.Fprintf(&b, "Roles: %d  Users: %d  Groups: %d  Policies: %d\n", r.Roles, r.Users, r.Groups, r.Policies)
	if d.Baseline {
		b.WriteString("Changes: first snapshot, no baseline to compare with\n")
	} else {
		fmt.Fprintf(&b, "Changes since previous snapshot: %d\n", d.Total())
	}
	fmt.Fprintf(&b, "\nFindings (%d):\n", len(fs))
	const max = 25
	for i, f := range fs {
		if i == max {
			fmt.Fprintf(&b, "... and %d more, see s3 findings/dt=%s/findings.json\n", len(fs)-max, dt)
			break
		}
		fmt.Fprintf(&b, "- [%s] %s %s %s: %s\n", f.Severity, f.Rule, f.ResourceType, f.ResourceName, f.Detail)
	}
	return b.String()
}
