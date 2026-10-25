package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

const githubOIDCURL = "token.actions.githubusercontent.com"

type GithubOIDCArgs struct {
	Name             string
	GithubRepo       string // owner/repo allowed to assume the roles
	ApplyEnvironment string // GitHub Environment (with required reviewers) that gates apply; defaults to production
	// StateBucketName scopes the CI roles' access to the Pulumi state backend (s3://<bucket>).
	StateBucketName string
	// CreateOIDCProvider: false if the account already has the provider (only one is allowed).
	CreateOIDCProvider bool
}

// GithubOIDC lets GitHub Actions assume AWS roles with no static keys: a read-only role for PRs
// and an admin role that only the approval-gated environment can assume.
type GithubOIDC struct {
	pulumi.ResourceState

	PlanRoleArn  pulumi.StringOutput
	ApplyRoleArn pulumi.StringOutput
}

func NewGithubOIDC(ctx *pulumi.Context, name string, args GithubOIDCArgs, opts ...pulumi.ResourceOption) (*GithubOIDC, error) {
	if args.ApplyEnvironment == "" {
		args.ApplyEnvironment = "production"
	}
	c := &GithubOIDC{}
	if err := ctx.RegisterComponentResource("iam-inventory:index:GithubOIDC", name, c, opts...); err != nil {
		return nil, err
	}
	parent := pulumi.Parent(c)

	var providerArn pulumi.StringInput
	if args.CreateOIDCProvider {
		p, err := iam.NewOpenIdConnectProvider(ctx, name+"-provider", &iam.OpenIdConnectProviderArgs{
			Url:           pulumi.String("https://" + githubOIDCURL),
			ClientIdLists: pulumi.StringArray{pulumi.String("sts.amazonaws.com")},
		}, parent)
		if err != nil {
			return nil, err
		}
		providerArn = p.Arn
	} else {
		p, err := iam.LookupOpenIdConnectProvider(ctx, &iam.LookupOpenIdConnectProviderArgs{
			Url: pulumi.StringRef("https://" + githubOIDCURL),
		}, pulumi.Parent(c))
		if err != nil {
			return nil, err
		}
		providerArn = pulumi.String(p.Arn)
	}

	trust := func(sub string) pulumi.StringOutput {
		return pulumi.JSONMarshal(pulumi.Map{
			"Version": pulumi.String("2012-10-17"),
			"Statement": pulumi.Array{pulumi.Map{
				"Effect":    pulumi.String("Allow"),
				"Action":    pulumi.String("sts:AssumeRoleWithWebIdentity"),
				"Principal": pulumi.Map{"Federated": providerArn},
				"Condition": pulumi.Map{"StringEquals": pulumi.Map{
					githubOIDCURL + ":aud": pulumi.String("sts.amazonaws.com"),
					githubOIDCURL + ":sub": pulumi.String(sub),
				}},
			}},
		})
	}

	plan, err := iam.NewRole(ctx, name+"-plan", &iam.RoleArgs{
		Name:               pulumi.Sprintf("%s-gha-plan", args.Name),
		AssumeRolePolicy:   trust("repo:" + args.GithubRepo + ":pull_request"),
		MaxSessionDuration: pulumi.Int(3600),
	}, parent)
	if err != nil {
		return nil, err
	}
	if _, err = iam.NewRolePolicyAttachment(ctx, name+"-plan-readonly", &iam.RolePolicyAttachmentArgs{
		Role:      plan.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/ReadOnlyAccess"),
	}, parent); err != nil {
		return nil, err
	}

	// `pulumi preview`/`up` read and write checkpoints in the S3 backend.
	stateAccess := pulumi.JSONMarshal(pulumi.Map{
		"Version": pulumi.String("2012-10-17"),
		"Statement": pulumi.Array{
			pulumi.Map{
				"Effect":   pulumi.String("Allow"),
				"Action":   pulumi.StringArray{pulumi.String("s3:GetObject"), pulumi.String("s3:PutObject"), pulumi.String("s3:DeleteObject")},
				"Resource": pulumi.String("arn:aws:s3:::" + args.StateBucketName + "/*"),
			},
			pulumi.Map{
				"Effect":   pulumi.String("Allow"),
				"Action":   pulumi.String("s3:ListBucket"),
				"Resource": pulumi.String("arn:aws:s3:::" + args.StateBucketName),
			},
		},
	})
	if _, err = iam.NewRolePolicy(ctx, name+"-plan-state", &iam.RolePolicyArgs{
		Name:   pulumi.String("pulumi-state"),
		Role:   plan.ID(),
		Policy: stateAccess,
	}, parent); err != nil {
		return nil, err
	}

	// Jobs that declare `environment:` get this subject instead of the branch ref. The environment
	// itself is restricted to main and needs a human approval (configure in GitHub, see docs).
	apply, err := iam.NewRole(ctx, name+"-apply", &iam.RoleArgs{
		Name:               pulumi.Sprintf("%s-gha-apply", args.Name),
		AssumeRolePolicy:   trust("repo:" + args.GithubRepo + ":environment:" + args.ApplyEnvironment),
		MaxSessionDuration: pulumi.Int(3600),
	}, parent)
	if err != nil {
		return nil, err
	}
	// Applying creates IAM roles, so this role is effectively admin. The blast radius is limited by the
	// trust policy (one repo, one approval-gated environment), not by the permissions.
	if _, err = iam.NewRolePolicyAttachment(ctx, name+"-apply-admin", &iam.RolePolicyAttachmentArgs{
		Role:      apply.Name,
		PolicyArn: pulumi.String("arn:aws:iam::aws:policy/AdministratorAccess"),
	}, parent); err != nil {
		return nil, err
	}

	c.PlanRoleArn, c.ApplyRoleArn = plan.Arn, apply.Arn
	if err := ctx.RegisterResourceOutputs(c, pulumi.Map{"planRoleArn": c.PlanRoleArn, "applyRoleArn": c.ApplyRoleArn}); err != nil {
		return nil, err
	}
	return c, nil
}
