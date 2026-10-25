// Command dev is the Pulumi program for the dev environment (replaces terraform/envs/dev).
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sns"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"github.com/saluja-ji/iam-inventory/pulumi/components"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		environment := cfg.Get("environment")
		if environment == "" {
			environment = "dev"
		}
		alertEmail := cfg.Require("alertEmail")
		lambdaSourceDir := orDefault(cfg.Get("lambdaSourceDir"), "../../build/lambda")
		githubRepo := cfg.Get("githubRepo") // empty skips the GitHub OIDC roles
		var extraEnv map[string]string
		if err := cfg.GetObject("lambdaExtraEnvironment", &extraEnv); err != nil {
			return err
		}

		provider, err := aws.NewProvider(ctx, "aws", &aws.ProviderArgs{
			DefaultTags: &aws.ProviderDefaultTagsArgs{
				Tags: pulumi.StringMap{
					"Project":     pulumi.String("iam-inventory"),
					"Environment": pulumi.String(environment),
					"ManagedBy":   pulumi.String("pulumi"),
				},
			},
		})
		if err != nil {
			return err
		}
		withProvider := pulumi.Provider(provider)

		name := "iam-inventory-" + environment
		accountID := aws.GetCallerIdentityOutput(ctx, aws.GetCallerIdentityOutputArgs{}, pulumi.Provider(provider)).AccountId()

		dataBucket, err := components.NewBucket(ctx, "data", components.BucketArgs{
			Name: pulumi.Sprintf("%s-%s-data", name, accountID),
			ExpiryRules: []components.ExpiryRule{
				{ID: "expire-raw", Prefix: "raw/", ExpireIn: intOr(cfg.GetInt("rawRetentionDays"), 365)},
			},
		}, withProvider)
		if err != nil {
			return err
		}

		athenaBucket, err := components.NewBucket(ctx, "athena", components.BucketArgs{
			Name:         pulumi.Sprintf("%s-%s-athena", name, accountID),
			ForceDestroy: true,
			ExpiryRules: []components.ExpiryRule{
				{ID: "expire-results", Prefix: "athena/", ExpireIn: 30},
			},
		}, withProvider)
		if err != nil {
			return err
		}

		// CloudWatch alarms cannot publish to a topic encrypted with the AWS-managed SNS key;
		// a CMK is a stretch goal.
		alerts, err := sns.NewTopic(ctx, "alerts", &sns.TopicArgs{
			Name: pulumi.Sprintf("%s-alerts", name),
		}, withProvider)
		if err != nil {
			return err
		}
		if _, err = sns.NewTopicSubscription(ctx, "alerts-email", &sns.TopicSubscriptionArgs{
			Topic:    alerts.Arn,
			Protocol: pulumi.String("email"),
			Endpoint: pulumi.String(alertEmail),
		}, withProvider); err != nil {
			return err
		}

		fn, err := components.NewLambda(ctx, "lambda", components.LambdaArgs{
			FunctionName:     name,
			SourceDir:        lambdaSourceDir,
			DataBucketArn:    dataBucket.Arn,
			DataBucketName:   dataBucket.ID,
			SnsTopicArn:      alerts.Arn,
			LogRetentionDays: intOr(cfg.GetInt("logRetentionDays"), 30),
			AlertMinSeverity: orDefault(cfg.Get("alertMinSeverity"), "high"),
			AdotLayerArn:     cfg.Get("adotLayerArn"),
			ExtraEnvironment: extraEnv,
		}, withProvider)
		if err != nil {
			return err
		}

		if _, err = components.NewScheduler(ctx, "scheduler", components.SchedulerArgs{
			Name:      name,
			LambdaArn: fn.FunctionArn,
			// 6 PM India Standard Time, every day.
			Cron:     "cron(0 18 * * ? *)",
			Timezone: "Asia/Kolkata",
		}, withProvider); err != nil {
			return err
		}

		glue, err := components.NewGlue(ctx, "glue", components.GlueArgs{
			Name:              name,
			DataBucketName:    dataBucket.ID,
			ResultsBucketName: athenaBucket.ID,
			ProjectionStart:   orDefault(cfg.Get("projectionStart"), "2026-01-01"),
			ScanLimitBytes:    intOr(cfg.GetInt("athenaScanLimitBytes"), 1<<30),
		}, withProvider)
		if err != nil {
			return err
		}

		if _, err = components.NewMonitoring(ctx, "monitoring", components.MonitoringArgs{
			Name:         name,
			FunctionName: fn.FunctionName,
			DlqName:      fn.DlqName,
			SnsTopicArn:  alerts.Arn,
		}, withProvider); err != nil {
			return err
		}

		ctx.Export("dataBucket", dataBucket.ID)
		ctx.Export("lambdaName", fn.FunctionName)
		ctx.Export("athenaWorkgroup", glue.Workgroup)
		ctx.Export("glueDatabase", glue.DatabaseName)
		ctx.Export("alertsTopicArn", alerts.Arn)

		if githubRepo != "" {
			oidc, err := components.NewGithubOIDC(ctx, "github-oidc", components.GithubOIDCArgs{
				Name:               name,
				GithubRepo:         githubRepo,
				StateBucketName:    cfg.Require("stateBucketName"),
				CreateOIDCProvider: cfg.GetBool("createGithubOidcProvider") || cfg.Get("createGithubOidcProvider") == "",
			}, withProvider)
			if err != nil {
				return err
			}
			ctx.Export("githubPlanRoleArn", oidc.PlanRoleArn)
			ctx.Export("githubApplyRoleArn", oidc.ApplyRoleArn)
		}
		return nil
	})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intOr(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
