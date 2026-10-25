// Command bootstrap creates the bucket that stores every other stack's Pulumi state (replaces terraform/bootstrap).
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

	"github.com/saluja-ji/iam-inventory/pulumi/components"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		stateBucketName := cfg.Require("stateBucketName")

		provider, err := aws.NewProvider(ctx, "aws", &aws.ProviderArgs{
			DefaultTags: &aws.ProviderDefaultTagsArgs{
				Tags: pulumi.StringMap{
					"Project":   pulumi.String("iam-inventory"),
					"ManagedBy": pulumi.String("pulumi"),
					"Stack":     pulumi.String("bootstrap"),
				},
			},
		})
		if err != nil {
			return err
		}

		// Same hardened bucket as the data lake (versioned, SSE, public access blocked, TLS-only),
		// with older state versions kept for 90 days. Protect is Pulumi's prevent_destroy.
		state, err := components.NewBucket(ctx, "state", components.BucketArgs{
			Name:                 pulumi.String(stateBucketName),
			NoncurrentExpireDays: 90,
		}, pulumi.Provider(provider), pulumi.Protect(true))
		if err != nil {
			return err
		}

		ctx.Export("stateBucket", state.ID)
		ctx.Export("backendUrl", pulumi.Sprintf("s3://%s", state.ID))
		return nil
	})
}
