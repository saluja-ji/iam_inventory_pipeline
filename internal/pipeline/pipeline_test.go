package pipeline

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric/noop"

	"github.com/saluja-ji/iam-inventory/internal/analyze"
	"github.com/saluja-ji/iam-inventory/internal/collector"
	"github.com/saluja-ji/iam-inventory/internal/store"
	"github.com/saluja-ji/iam-inventory/internal/telemetry"
)

type recNotifier struct{ subjects []string }

func (r *recNotifier) Notify(_ context.Context, s, _ string) error {
	r.subjects = append(r.subjects, s)
	return nil
}

type failNotifier struct{}

func (failNotifier) Notify(context.Context, string, string) error { return errors.New("sns down") }

func build(t *testing.T, root string, now time.Time, n interface {
	Notify(context.Context, string, string) error
}, emf *bytes.Buffer) *Pipeline {
	t.Helper()
	m, err := telemetry.NewMetrics(noop.NewMeterProvider().Meter("t"), "IAMInventory", "iam-inventory", emf)
	if err != nil {
		t.Fatal(err)
	}
	return &Pipeline{
		Source:   collector.FileSource{Path: "../../testdata/sample_iam.json"},
		Store:    store.Dir{Root: root},
		Notifier: n, Metrics: m,
		Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		Cfg: Config{AlertMinSeverity: analyze.High, MaxKeyAgeDays: 90},
		Now: func() time.Time { return now },
	}
}

func TestRunEndToEndAndIdempotent(t *testing.T) {
	root := t.TempDir()
	day1 := time.Date(2026, 10, 17, 12, 30, 0, 0, time.UTC)
	day2 := day1.AddDate(0, 0, 1)
	var emf bytes.Buffer
	n := &recNotifier{}

	r1, err := build(t, root, day1, n, &emf).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r1.Roles == 0 || r1.Findings == 0 || !r1.Alerted || r1.DT != "2026-10-17" {
		t.Fatalf("unexpected result: %+v", r1)
	}
	// Re-run the same day: still exactly one object per partition.
	if _, err := build(t, root, day1, n, &emf).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(root, "curated", "roles", "dt=2026-10-17", "*"))
	if len(files) != 1 {
		t.Fatalf("want 1 roles partition file, got %d", len(files))
	}
	for _, key := range []string{store.RawKey("2026-10-17"), store.FindingsKey("2026-10-17"), store.DiffKey("2026-10-17")} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(key))); err != nil {
			t.Fatalf("missing %s", key)
		}
	}

	// Next day: previous snapshot is found, so this is not a baseline.
	r2, err := build(t, root, day2, n, &emf).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if r2.DT != "2026-10-18" {
		t.Fatalf("dt: %s", r2.DT)
	}
	b, _ := os.ReadFile(filepath.Join(root, "diff", "dt=2026-10-18", "diff.json"))
	if !strings.Contains(string(b), `"baseline":false`) {
		t.Fatalf("expected non-baseline diff: %s", b)
	}
	if len(n.subjects) != 3 || !strings.Contains(emf.String(), `"run_success":1`) {
		t.Fatalf("notifications=%d emf=%s", len(n.subjects), emf.String())
	}
}

func TestFailedAlertFailsRunAndRecordsFailure(t *testing.T) {
	var emf bytes.Buffer
	_, err := build(t, t.TempDir(), time.Now(), failNotifier{}, &emf).Run(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(emf.String(), `"run_success":0`) {
		t.Fatalf("failure not recorded: %s", emf.String())
	}
}

func TestPartitionUsesIST(t *testing.T) {
	// 20:00 UTC is already the next day in IST.
	now := time.Date(2026, 10, 17, 20, 0, 0, 0, time.UTC)
	r, err := build(t, t.TempDir(), now, &recNotifier{}, &bytes.Buffer{}).Run(context.Background())
	if err != nil || r.DT != "2026-10-18" {
		t.Fatalf("dt=%s err=%v", r.DT, err)
	}
}
