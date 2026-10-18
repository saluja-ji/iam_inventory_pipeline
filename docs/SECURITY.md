# Security notes

## Checklist

- [x] No `.tfstate`, `*.tfvars`, `backend.hcl` or `out/` in git (see `.gitignore`)
- [x] All buckets: SSE (AES256), versioning, Block Public Access, TLS-only bucket policy
- [x] Lambda role limited to: IAM read for two actions, S3 on four prefixes, its own log group, SNS publish to one topic, SQS send to its DLQ (plus X-Ray/log-stream permissions only when OTel is on)
- [x] OIDC trust policies pin the exact repository and either `pull_request` (plan role) or one gated environment (apply role). No AWS keys in GitHub.
- [x] Log retention 30 days, DLQ encrypted
- [x] Sample data is synthetic (`testdata/sample_iam.json`)
- [ ] You: confirm nothing from your real account is committed before making the repo public (step 12 in [USER_ACTIONS.md](USER_ACTIONS.md))

## Accepted risks (and the checkov skips in `ci.yml`)

The `skip_check` list in `.github/workflows/ci.yml` was written without running checkov. Run `checkov -d terraform` yourself and keep only skips you can justify. The likely, defensible ones:

- Lambda not in a VPC, no code signing, no reserved concurrency: single-purpose function that only calls AWS APIs; VPC adds cost and NAT complexity for no benefit.
- SNS topic not encrypted with a CMK: CloudWatch alarms cannot publish to a topic using the AWS-managed key.
- S3 access logging / cross-region replication / event notifications: out of scope for a sandbox data lake.
- Lambda env vars not KMS-encrypted: they hold only a bucket name and a topic ARN.

## Data sensitivity

The raw snapshot contains your full IAM configuration, which is sensitive reconnaissance data. The bucket is private and encrypted; Athena results expire after 30 days. Never publish real snapshots, screenshots with account IDs, or the Terraform state. Access key IDs are collected but never secrets.

## The apply role

`...-gha-apply` has `AdministratorAccess` because Terraform must create IAM roles. Mitigations: only one repository can assume it, only from the `production` environment that requires human approval and is restricted to `main`. For a stricter setup, replace the managed policy with a custom one scoped to the resources in this stack, with a permissions boundary.
