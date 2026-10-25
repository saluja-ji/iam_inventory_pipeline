# Deploying with Pulumi

The Pulumi program (Go) in [pulumi/](../pulumi) replaces the Terraform in [terraform/](../terraform). Why the switch and what is different: [TERRAFORM_VS_PULUMI.md](TERRAFORM_VS_PULUMI.md).

## Layout

| Terraform | Pulumi |
| --- | --- |
| `terraform/bootstrap` | [pulumi/bootstrap](../pulumi/bootstrap): the state bucket, protected from deletion |
| `terraform/envs/dev` | [pulumi/dev](../pulumi/dev): the stack wiring everything together |
| `modules/s3` | [components/bucket.go](../pulumi/components/bucket.go) |
| `modules/lambda` | [components/lambda.go](../pulumi/components/lambda.go) |
| `modules/scheduler` | [components/scheduler.go](../pulumi/components/scheduler.go) |
| `modules/glue` + `schemas.json` | [components/glue.go](../pulumi/components/glue.go), schema read from `transform.Schemas()` |
| `modules/monitoring` | [components/monitoring.go](../pulumi/components/monitoring.go) |
| `modules/github_oidc` | [components/github_oidc.go](../pulumi/components/github_oidc.go) |
| `terraform/sandbox_identities` | [pulumi/sandbox_identities](../pulumi/sandbox_identities) |
| `*.tfvars` | `pulumi config set` / `Pulumi.dev.yaml` (see `Pulumi.dev.yaml.example`) |

`pulumi/` is its own Go module (it imports the root module for the schemas), so it does not add dependencies to the Lambda build.

## Offline checks (no AWS, no Pulumi login)

```bash
make pulumi-test     # go vet + mocked resource tests
```

## One-time bootstrap

Needs the [Pulumi CLI](https://www.pulumi.com/docs/install/) and AWS credentials.

```bash
cd pulumi/bootstrap
pulumi login --local                       # bootstrap state stays on disk
export PULUMI_CONFIG_PASSPHRASE=...        # choose one and keep it
pulumi stack init bootstrap
pulumi config set aws:region ap-south-1
pulumi config set stateBucketName <globally-unique-name>
pulumi up
```

## Deploy the dev stack

```bash
make package
cd pulumi/dev
pulumi login "s3://<state-bucket>?region=ap-south-1"
export PULUMI_CONFIG_PASSPHRASE=...        # same value CI will use
pulumi stack init dev
cp Pulumi.dev.yaml.example Pulumi.dev.yaml # then edit it
pulumi preview
pulumi up
```

Confirm the SNS email subscription when the message arrives.

## CI/CD changes

[plan.yml](../.github/workflows/plan.yml) runs `pulumi preview` and comments on the PR; [apply.yml](../.github/workflows/apply.yml) runs `pulumi up` behind the `production` environment. The OIDC roles and approval gate are unchanged. New repository settings:

| Kind | Name | Value |
| --- | --- | --- |
| variable | `PULUMI_STATE_BUCKET` | replaces `TF_STATE_BUCKET` |
| secret | `PULUMI_CONFIG_PASSPHRASE` | encrypts stack secrets |

`AWS_REGION`, `AWS_PLAN_ROLE_ARN`, `AWS_APPLY_ROLE_ARN`, `ALERT_EMAIL`, `ADOT_LAYER_ARN` are reused. Bootstrapping the OIDC roles for the very first time still needs a local `pulumi up` with admin credentials.

## Config reference (`iam-inventory:` namespace)

| Key | Default | Meaning |
| --- | --- | --- |
| `alertEmail` | required | alert and findings recipient |
| `environment` | `dev` | name suffix and tag |
| `lambdaSourceDir` | `../../build/lambda` | output of `make package`, relative to `pulumi/dev` |
| `adotLayerArn` | empty | enables OpenTelemetry export |
| `alertMinSeverity` | `high` | |
| `rawRetentionDays` | `365` | |
| `logRetentionDays` | `30` | |
| `githubRepo` | empty | `owner/repo`; empty skips the OIDC roles |
| `createGithubOidcProvider` | `true` | set `false` if the account already has the provider |
| `stateBucketName` | required with `githubRepo` | scopes the CI roles to the state bucket |
| `athenaScanLimitBytes` | `1073741824` | |
| `projectionStart` | `2026-01-01` | |
| `lambdaExtraEnvironment` | `{}` | JSON map, e.g. `{"MAX_KEY_AGE_DAYS":"1"}` |

## Legacy Terraform

The `terraform/` directory is left in place for comparison and is still checked by CI. Do not apply both against the same account. Once you are happy with Pulumi, delete `terraform/`, `cmd/schemagen`, `make schema`, and the schema and Terraform steps in [ci.yml](../.github/workflows/ci.yml).
