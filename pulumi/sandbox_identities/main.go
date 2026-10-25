// Command sandbox_identities creates deliberately risky identities for the analyzer to flag
// (replaces terraform/sandbox_identities). SANDBOX ACCOUNTS ONLY: deploy as its own stack and destroy when done.
package main

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg := config.New(ctx, "")
		externalAccountID := cfg.Get("externalAccountId") // any other 12-digit account id; nobody needs to own it
		if externalAccountID == "" {
			externalAccountID = "999999999999"
		}

		provider, err := aws.NewProvider(ctx, "aws", &aws.ProviderArgs{
			DefaultTags: &aws.ProviderDefaultTagsArgs{
				Tags: pulumi.StringMap{
					"Project":   pulumi.String("iam-inventory"),
					"Purpose":   pulumi.String("analyzer-test-data"),
					"ManagedBy": pulumi.String("pulumi"),
				},
			},
		})
		if err != nil {
			return err
		}
		p := pulumi.Provider(provider)

		trustLambda := pulumi.JSONMarshal(pulumi.Map{
			"Version": pulumi.String("2012-10-17"),
			"Statement": pulumi.Array{pulumi.Map{
				"Effect":    pulumi.String("Allow"),
				"Action":    pulumi.String("sts:AssumeRole"),
				"Principal": pulumi.Map{"Service": pulumi.String("lambda.amazonaws.com")},
			}},
		})
		role := func(id, name string, trust pulumi.StringInput, boundary pulumi.StringPtrInput) (*iam.Role, error) {
			return iam.NewRole(ctx, id, &iam.RoleArgs{
				Name:                pulumi.String(name),
				AssumeRolePolicy:    trust,
				PermissionsBoundary: boundary,
			}, p)
		}
		inline := func(id, policyName string, r *iam.Role, statement pulumi.Map) error {
			_, err := iam.NewRolePolicy(ctx, id, &iam.RolePolicyArgs{
				Name: pulumi.String(policyName),
				Role: r.ID(),
				Policy: pulumi.JSONMarshal(pulumi.Map{
					"Version":   pulumi.String("2012-10-17"),
					"Statement": pulumi.Array{statement},
				}),
			}, p)
			return err
		}
		attach := func(id string, r *iam.Role, arn string) error {
			_, err := iam.NewRolePolicyAttachment(ctx, id, &iam.RolePolicyAttachmentArgs{
				Role: r.Name, PolicyArn: pulumi.String(arn),
			}, p)
			return err
		}

		// Expect: ADMIN_POLICY_ATTACHED
		admin, err := role("test-admin", "iaminv-test-admin", trustLambda, nil)
		if err != nil {
			return err
		}
		if err = attach("test-admin", admin, "arn:aws:iam::aws:policy/AdministratorAccess"); err != nil {
			return err
		}

		// Expect: no findings
		readonly, err := role("test-readonly", "iaminv-test-readonly", trustLambda, nil)
		if err != nil {
			return err
		}
		if err = attach("test-readonly", readonly, "arn:aws:iam::aws:policy/ReadOnlyAccess"); err != nil {
			return err
		}

		// Expect: WILDCARD_ADMIN
		wildcard, err := role("test-wildcard", "iaminv-test-wildcard", trustLambda, nil)
		if err != nil {
			return err
		}
		if err = inline("test-wildcard", "everything", wildcard, pulumi.Map{
			"Effect": pulumi.String("Allow"), "Action": pulumi.String("*"), "Resource": pulumi.String("*"),
		}); err != nil {
			return err
		}

		// Expect: PASSROLE_BROAD_RESOURCE
		passrole, err := role("test-passrole", "iaminv-test-passrole", trustLambda, nil)
		if err != nil {
			return err
		}
		if err = inline("test-passrole", "passrole", passrole, pulumi.Map{
			"Effect":   pulumi.String("Allow"),
			"Action":   pulumi.StringArray{pulumi.String("iam:PassRole"), pulumi.String("ecs:*")},
			"Resource": pulumi.String("*"),
		}); err != nil {
			return err
		}

		// Expect: TRUST_EXTERNAL_ACCOUNT (high, no condition)
		if _, err = role("test-external-trust", "iaminv-test-external-trust", pulumi.JSONMarshal(pulumi.Map{
			"Version": pulumi.String("2012-10-17"),
			"Statement": pulumi.Array{pulumi.Map{
				"Effect":    pulumi.String("Allow"),
				"Action":    pulumi.String("sts:AssumeRole"),
				"Principal": pulumi.Map{"AWS": pulumi.Sprintf("arn:aws:iam::%s:root", externalAccountID)},
			}},
		}), nil); err != nil {
			return err
		}

		// Expect: TRUST_ANY_PRINCIPAL at medium severity (the org condition lowers it from critical).
		if _, err = role("test-conditional-wildcard", "iaminv-test-conditional-wildcard", pulumi.JSONMarshal(pulumi.Map{
			"Version": pulumi.String("2012-10-17"),
			"Statement": pulumi.Array{pulumi.Map{
				"Effect":    pulumi.String("Allow"),
				"Action":    pulumi.String("sts:AssumeRole"),
				"Principal": pulumi.Map{"AWS": pulumi.String("*")},
				"Condition": pulumi.Map{"StringEquals": pulumi.Map{"aws:PrincipalOrgID": pulumi.String("o-exampleorgid")}},
			}},
		}), nil); err != nil {
			return err
		}

		// Unused role with a permissions boundary and an inline policy, for attachment-table coverage.
		boundary, err := iam.NewPolicy(ctx, "boundary", &iam.PolicyArgs{
			Name: pulumi.String("iaminv-test-boundary"),
			Policy: pulumi.JSONMarshal(pulumi.Map{
				"Version": pulumi.String("2012-10-17"),
				"Statement": pulumi.Array{pulumi.Map{
					"Effect":   pulumi.String("Allow"),
					"Action":   pulumi.StringArray{pulumi.String("s3:Get*"), pulumi.String("s3:List*")},
					"Resource": pulumi.String("*"),
				}},
			}),
		}, p)
		if err != nil {
			return err
		}
		if _, err = role("test-unused-bounded", "iaminv-test-unused-bounded", trustLambda, boundary.Arn); err != nil {
			return err
		}

		// A user to create an access key for by hand (`aws iam create-access-key`). OLD_ACCESS_KEY only fires
		// after 90 days; to test sooner set MAX_KEY_AGE_DAYS=1 in the Lambda environment.
		if _, err = iam.NewUser(ctx, "test-user", &iam.UserArgs{Name: pulumi.String("iaminv-test-user")}, p); err != nil {
			return err
		}

		ctx.Export("accountId", aws.GetCallerIdentityOutput(ctx, aws.GetCallerIdentityOutputArgs{}, p).AccountId())
		return nil
	})
}
