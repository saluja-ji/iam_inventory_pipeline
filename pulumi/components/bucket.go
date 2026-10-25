// Package components holds the reusable building blocks that replace the Terraform modules.
package components

import (
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ExpiryRule expires objects under Prefix after ExpireIn days.
type ExpiryRule struct {
	ID       string
	Prefix   string
	ExpireIn int
}

type BucketArgs struct {
	Name                 pulumi.StringInput
	ForceDestroy         bool
	ExpiryRules          []ExpiryRule
	NoncurrentExpireDays int // defaults to 30
}

// Bucket is a private, versioned, SSE-encrypted S3 bucket that denies non-TLS access.
type Bucket struct {
	pulumi.ResourceState

	Bucket *s3.Bucket
	ID     pulumi.StringOutput
	Arn    pulumi.StringOutput
}

func NewBucket(ctx *pulumi.Context, name string, args BucketArgs, opts ...pulumi.ResourceOption) (*Bucket, error) {
	c := &Bucket{}
	if err := ctx.RegisterComponentResource("iam-inventory:index:Bucket", name, c, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(c)
	if args.NoncurrentExpireDays == 0 {
		args.NoncurrentExpireDays = 30
	}

	b, err := s3.NewBucket(ctx, name, &s3.BucketArgs{
		Bucket:       args.Name,
		ForceDestroy: pulumi.Bool(args.ForceDestroy),
	}, parent)
	if err != nil {
		return nil, err
	}

	versioning, err := s3.NewBucketVersioning(ctx, name+"-versioning", &s3.BucketVersioningArgs{
		Bucket: b.ID(),
		VersioningConfiguration: &s3.BucketVersioningVersioningConfigurationArgs{
			Status: pulumi.String("Enabled"),
		},
	}, parent)
	if err != nil {
		return nil, err
	}

	if _, err = s3.NewBucketServerSideEncryptionConfiguration(ctx, name+"-sse", &s3.BucketServerSideEncryptionConfigurationArgs{
		Bucket: b.ID(),
		Rules: s3.BucketServerSideEncryptionConfigurationRuleArray{
			&s3.BucketServerSideEncryptionConfigurationRuleArgs{
				ApplyServerSideEncryptionByDefault: &s3.BucketServerSideEncryptionConfigurationRuleApplyServerSideEncryptionByDefaultArgs{
					SseAlgorithm: pulumi.String("AES256"),
				},
			},
		},
	}, parent); err != nil {
		return nil, err
	}

	pab, err := s3.NewBucketPublicAccessBlock(ctx, name+"-pab", &s3.BucketPublicAccessBlockArgs{
		Bucket:                b.ID(),
		BlockPublicAcls:       pulumi.Bool(true),
		BlockPublicPolicy:     pulumi.Bool(true),
		IgnorePublicAcls:      pulumi.Bool(true),
		RestrictPublicBuckets: pulumi.Bool(true),
	}, parent)
	if err != nil {
		return nil, err
	}

	rules := s3.BucketLifecycleConfigurationRuleArray{
		&s3.BucketLifecycleConfigurationRuleArgs{
			Id:     pulumi.String("housekeeping"),
			Status: pulumi.String("Enabled"),
			Filter: &s3.BucketLifecycleConfigurationRuleFilterArgs{},
			NoncurrentVersionExpiration: &s3.BucketLifecycleConfigurationRuleNoncurrentVersionExpirationArgs{
				NoncurrentDays: pulumi.Int(args.NoncurrentExpireDays),
			},
			AbortIncompleteMultipartUpload: &s3.BucketLifecycleConfigurationRuleAbortIncompleteMultipartUploadArgs{
				DaysAfterInitiation: pulumi.Int(7),
			},
		},
	}
	for _, r := range args.ExpiryRules {
		rules = append(rules, &s3.BucketLifecycleConfigurationRuleArgs{
			Id:         pulumi.String(r.ID),
			Status:     pulumi.String("Enabled"),
			Filter:     &s3.BucketLifecycleConfigurationRuleFilterArgs{Prefix: pulumi.String(r.Prefix)},
			Expiration: &s3.BucketLifecycleConfigurationRuleExpirationArgs{Days: pulumi.Int(r.ExpireIn)},
		})
	}
	if _, err = s3.NewBucketLifecycleConfiguration(ctx, name+"-lifecycle", &s3.BucketLifecycleConfigurationArgs{
		Bucket: b.ID(),
		Rules:  rules,
	}, parent, pulumi.DependsOn([]pulumi.Resource{versioning})); err != nil {
		return nil, err
	}

	policy := pulumi.JSONMarshal(pulumi.Map{
		"Version": pulumi.String("2012-10-17"),
		"Statement": pulumi.Array{pulumi.Map{
			"Sid":       pulumi.String("DenyInsecureTransport"),
			"Effect":    pulumi.String("Deny"),
			"Principal": pulumi.String("*"),
			"Action":    pulumi.String("s3:*"),
			"Resource": pulumi.Array{
				b.Arn,
				pulumi.Sprintf("%s/*", b.Arn),
			},
			"Condition": pulumi.Map{"Bool": pulumi.Map{"aws:SecureTransport": pulumi.String("false")}},
		}},
	})
	if _, err = s3.NewBucketPolicy(ctx, name+"-policy", &s3.BucketPolicyArgs{
		Bucket: b.ID(),
		Policy: policy,
	}, parent, pulumi.DependsOn([]pulumi.Resource{pab})); err != nil {
		return nil, err
	}

	c.Bucket, c.ID, c.Arn = b, b.ID().ToStringOutput(), b.Arn
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{"id": c.ID, "arn": c.Arn}); err != nil {
		return nil, fmt.Errorf("bucket %s outputs: %w", name, err)
	}
	return c, nil
}
