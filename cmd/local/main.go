// Command local runs the same pipeline outside Lambda.
//
//	go run ./cmd/local -fixture testdata/sample_iam.json -out ./out   # offline, no AWS needed
//	go run ./cmd/local -out ./out                                      # real IAM, local output dir
//	go run ./cmd/local -bucket my-bucket                               # real IAM, real S3
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.opentelemetry.io/otel"

	"github.com/saluja-ji/iam-inventory/internal/analyze"
	"github.com/saluja-ji/iam-inventory/internal/collector"
	"github.com/saluja-ji/iam-inventory/internal/notify"
	"github.com/saluja-ji/iam-inventory/internal/pipeline"
	"github.com/saluja-ji/iam-inventory/internal/store"
	"github.com/saluja-ji/iam-inventory/internal/telemetry"
)

func main() {
	fixture := flag.String("fixture", "", "replay a saved snapshot JSON instead of calling AWS")
	out := flag.String("out", "./out", "local output directory (ignored when -bucket is set)")
	bucket := flag.String("bucket", "", "write to this S3 bucket instead of a local directory")
	minSev := flag.String("min-severity", "high", "print findings at or above this severity")
	flag.Parse()

	if err := run(*fixture, *out, *bucket, *minSev); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(fixture, out, bucket, minSev string) error {
	ctx := context.Background()
	log := slog.New(telemetry.NewTraceHandler(slog.NewTextHandler(os.Stderr, nil)))

	prov, err := telemetry.Init(ctx, "iam-inventory-local", "dev")
	if err != nil {
		return err
	}
	defer func() { _ = prov.Shutdown(ctx) }()

	var (
		src collector.Source
		bk  store.Backend = store.Dir{Root: out}
	)
	if fixture != "" {
		src = collector.FileSource{Path: fixture}
	}
	if fixture == "" || bucket != "" {
		cfg, err := config.LoadDefaultConfig(ctx)
		if err != nil {
			return err
		}
		otelaws.AppendMiddlewares(&cfg.APIOptions)
		if fixture == "" {
			id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
			if err != nil {
				return fmt.Errorf("no usable AWS credentials (try `aws sts get-caller-identity`): %w", err)
			}
			src = &collector.Collector{IAM: iam.NewFromConfig(cfg), AccountID: aws.ToString(id.Account)}
		}
		if bucket != "" {
			bk = store.S3{Client: s3.NewFromConfig(cfg), Bucket: bucket}
		}
	}

	metrics, err := telemetry.NewMetrics(otel.Meter("iam-inventory-local"), "IAMInventory", "iam-inventory-local", nil)
	if err != nil {
		return err
	}
	p := &pipeline.Pipeline{
		Source: src, Store: bk, Notifier: notify.Noop{}, Metrics: metrics, Log: log,
		Cfg: pipeline.Config{AlertMinSeverity: analyze.ParseSeverity(minSev, analyze.High), MaxKeyAgeDays: 90},
		Now: time.Now,
	}
	res, err := p.Run(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("run %s dt=%s roles=%d users=%d groups=%d policies=%d findings=%d changes=%d\n",
		res.RunID, res.DT, res.Roles, res.Users, res.Groups, res.Policies, res.Findings, res.Changes)
	if bucket == "" {
		fmt.Println("output written under", out)
	}
	return prov.Flush(ctx)
}
