// Command lambda is the scheduled entrypoint: it snapshots IAM into S3 and raises alerts.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
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

const (
	serviceName = "iam-inventory"
	namespace   = "IAMInventory"
)

var version = "dev" // set with -ldflags "-X main.version=..."

func main() {
	ctx := context.Background()
	log := slog.New(telemetry.NewTraceHandler(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(log)

	h, err := newHandler(ctx, log)
	if err != nil {
		log.Error("init failed", "error", err)
		os.Exit(1)
	}
	lambda.Start(h)
}

func newHandler(ctx context.Context, log *slog.Logger) (func(context.Context, json.RawMessage) (pipeline.Result, error), error) {
	bucket := mustEnv("DATA_BUCKET")

	prov, err := telemetry.Init(ctx, serviceName, version)
	if err != nil {
		return nil, err
	}

	// Throttling on IAM is the main transient failure, so retry generously with capped backoff.
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer {
		return retry.NewStandard(func(o *retry.StandardOptions) {
			o.MaxAttempts = 8
			o.MaxBackoff = 20 * time.Second
		})
	}))
	if err != nil {
		return nil, err
	}
	otelaws.AppendMiddlewares(&cfg.APIOptions) // one span per AWS API call

	acct, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, err
	}

	var n notify.Notifier = notify.Noop{}
	if arn := os.Getenv("SNS_TOPIC_ARN"); arn != "" {
		n = notify.SNS{Client: sns.NewFromConfig(cfg), TopicARN: arn}
	}
	metrics, err := telemetry.NewMetrics(otel.Meter(serviceName), namespace, serviceName, os.Stdout)
	if err != nil {
		return nil, err
	}

	p := &pipeline.Pipeline{
		Source: &collector.Collector{
			IAM: iam.NewFromConfig(cfg), AccountID: aws.ToString(acct.Account),
			IncludeUnattachedAWSPolicies: envBool("INCLUDE_UNATTACHED_AWS_POLICIES"),
		},
		Store:    store.S3{Client: s3.NewFromConfig(cfg), Bucket: bucket},
		Notifier: n,
		Metrics:  metrics,
		Log:      log,
		Cfg: pipeline.Config{
			AlertMinSeverity: analyze.ParseSeverity(os.Getenv("ALERT_MIN_SEVERITY"), analyze.High),
			MaxKeyAgeDays:    envInt("MAX_KEY_AGE_DAYS", 90),
		},
		Now: time.Now,
	}

	return func(ctx context.Context, _ json.RawMessage) (pipeline.Result, error) {
		res, err := p.Run(ctx)
		// Lambda freezes right after returning, so push telemetry out before that happens.
		fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if ferr := prov.Flush(fctx); ferr != nil {
			log.Warn("telemetry flush failed", "error", ferr)
		}
		return res, err
	}, nil
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		slog.Error("missing required environment variable", "name", k)
		os.Exit(1)
	}
	return v
}

func envBool(k string) bool { b, _ := strconv.ParseBool(os.Getenv(k)); return b }

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return n
	}
	return def
}
