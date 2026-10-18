# AWS IAM Inventory Pipeline: Project Plan

A Go + Terraform project that collects IAM roles, users, groups and policies from AWS every day at 6 PM IST, stores them as a queryable history in S3, and alerts on risky permissions. Built to practice real DevOps and data engineering workflows end to end.

## 1. Goals

- Fetch IAM roles, users, groups, and policies using the AWS Go SDK v2.
- Deploy everything with Terraform. No manual console configuration, no static credentials.
- Run automatically every day at 6 PM IST.
- Keep daily snapshots so changes over time can be queried.
- Practice: IaC, CI/CD, least privilege, observability, data lake design, security scanning, testing.

**Out of scope for v1:** multi-account, non-AWS sources, dashboards. These are stretch goals in section 10.

## 2. Architecture

```
EventBridge Scheduler (cron 0 18 * * ? *, Asia/Kolkata)
        |
        v
Lambda (Go, provided.al2023, arm64)
        |  iam:GetAccountAuthorizationDetails (paginated)
        v
S3 raw layer      s3://<bucket>/raw/dt=YYYY-MM-DD/iam.json
        |
        v  (transform step in the same Lambda or a second one)
S3 curated layer  s3://<bucket>/curated/<table>/dt=YYYY-MM-DD/*.parquet
        |
        v
Glue Data Catalog  ->  Athena (SQL over history)

CloudWatch Logs / Metrics / Alarms  ->  SNS email
SQS DLQ for failed invocations
```

## 3. Tech stack

| Area | Choice |
| --- | --- |
| Language | Go 1.22+, AWS SDK for Go v2 |
| IaC | Terraform with modules, S3 remote state |
| Compute | Lambda (`provided.al2023`, arm64) |
| Schedule | EventBridge Scheduler with timezone support |
| Storage | S3 (raw JSON, curated Parquet) |
| Query | Glue Data Catalog + Athena |
| CI/CD | GitHub Actions with OIDC to AWS |
| Quality | golangci-lint, tflint, checkov or tfsec, go test |
| Observability | slog JSON logs, CloudWatch alarms, SNS, DLQ |

## 4. Repository layout

```
iam-inventory/
  cmd/lambda/main.go          # Lambda entrypoint
  internal/
    collector/                # AWS IAM calls + pagination
    model/                    # normalized Identity / Policy structs
    transform/                # JSON -> flat rows -> Parquet
    store/                    # S3 writer (idempotent partition overwrite)
    analyze/                  # risk rules and diffing
  terraform/
    bootstrap/                # state bucket, run once locally
    modules/
      s3/  lambda/  scheduler/  glue/  monitoring/  github_oidc/
    envs/dev/                 # main.tf, variables.tf, dev.tfvars
  .github/workflows/
    ci.yml  plan.yml  apply.yml
  Makefile
  README.md
```

## 5. Phased delivery plan

### Phase 0: Setup (day 1)

- Create a dedicated sandbox AWS account or at least a separate profile. Never practice on a production account.
- Install Go, Terraform, AWS CLI, tflint, golangci-lint.
- Create the Git repo with a `.gitignore` covering `*.tfstate`, `*.tfvars` with account IDs, and build artifacts.

**Done when:** `aws sts get-caller-identity` works and the repo is initialized.

### Phase 1: Local collector (days 2 to 4)

- Write `collector` using `iam.NewGetAccountAuthorizationDetailsPaginator`.
- Decode URL-encoded policy documents with `url.QueryUnescape`.
- Define the normalized model: `Role`, `User`, `Group`, `Policy`, `Attachment`, `TrustPolicy`.
- Dump the result to a local JSON file.
- Add unit tests using a mocked IAM client interface.

**Done when:** running `go run ./cmd/local` produces a complete, valid JSON file of your account.

### Phase 2: Storage layer (days 5 to 6)

- Write `store` to upload to S3 under `raw/dt=YYYY-MM-DD/iam.json`.
- Use a deterministic key so re-runs on the same day overwrite, giving idempotency.
- Add a `collected_at` timestamp and a `run_id` to each snapshot.

**Done when:** two runs on the same day leave exactly one object per partition.

### Phase 3: Terraform foundation (days 7 to 10)

1. **Bootstrap config:** S3 bucket for Terraform state with versioning and encryption. Use S3 native locking (`use_lockfile = true`). Apply once locally.
2. **Data bucket module:** SSE encryption, Block Public Access, versioning, lifecycle rule to expire old raw files.
3. **Lambda module:** IAM execution role, function, log group with retention, DLQ.
4. **Scheduler module:** EventBridge Scheduler with `schedule_expression_timezone = "Asia/Kolkata"` and `cron(0 18 * * ? *)`, plus its own invoke role.
5. Build the Lambda with `GOOS=linux GOARCH=arm64 go build -tags lambda.norpc -o bootstrap` and zip it. Terraform references the zip.

**Least privilege for the Lambda role:**

- `iam:GetAccountAuthorizationDetails` (resource `*`, required by the API)
- `s3:PutObject` and `s3:GetObject` on only the data bucket prefixes
- `logs:CreateLogStream`, `logs:PutLogEvents` on its own log group
- `sqs:SendMessage` on the DLQ

**Done when:** `terraform apply` from a clean checkout creates everything and a manual test invoke writes a file to S3.

### Phase 4: CI/CD (days 11 to 14)

- Create the GitHub OIDC provider and a deploy role in Terraform. The trust policy must be restricted to your repo and branch (`repo:<user>/<repo>:ref:refs/heads/main`).
- `ci.yml` on every PR: `go vet`, `go test -race`, `golangci-lint`, `terraform fmt -check`, `terraform validate`, `tflint`, `checkov`.
- `plan.yml` on PR: `terraform plan` posted as a PR comment.
- `apply.yml` on merge to main: `terraform apply`, gated by a GitHub Environment approval.
- No AWS access keys stored in GitHub secrets.

**Done when:** merging a small change deploys automatically and the pipeline never uses a static credential.

### Phase 5: Data engineering layer (days 15 to 20)

- Flatten the nested JSON into tables: `roles`, `users`, `groups`, `policies`, `role_policy_attachments`, `trust_relationships`.
- Write them as Parquet (for example with `parquet-go`), partitioned by `dt`.
- Create the Glue database and tables in Terraform, with partition projection on `dt` so no crawler is needed.
- Create an Athena workgroup with a results bucket and a query scan limit.

**Example queries to validate:**

```sql
-- Roles with AdministratorAccess today
SELECT role_name FROM role_policy_attachments
WHERE dt = '2026-10-18' AND policy_name = 'AdministratorAccess';

-- Roles created in the last 7 days
SELECT role_name, create_date FROM roles
WHERE dt = '2026-10-18' AND create_date > current_date - interval '7' day;
```

**Done when:** you can answer "what changed since yesterday?" using only SQL.

### Phase 6: Analysis and alerts (days 21 to 26)

- Risk rules in `analyze`: wildcard `Action: *` with `Resource: *`, trust policies allowing external accounts or `Principal: *`, `iam:PassRole` combined with broad resources, users with access keys older than 90 days.
- Diff engine: compare today's snapshot with yesterday's and output added, removed, and modified roles and policies.
- Optional: call `GenerateServiceLastAccessedDetails` for unused-permission detection.
- Emit findings to S3 (`findings/dt=...`) and publish a summary to SNS.

**Done when:** deliberately creating a risky test role triggers an email the next day.

### Phase 7: Observability and hardening (days 27 to 30)

- Structured JSON logs with `slog` including `run_id`, counts, and duration.
- Custom CloudWatch metrics: `roles_collected`, `policies_collected`, `findings_count`.
- Alarms: Lambda errors greater than 0, no successful run in 26 hours (missing data treated as breaching), DLQ depth greater than 0.
- Retry with backoff for IAM throttling (use the SDK retryer settings).
- Write the README, an architecture diagram, and a runbook for failures.

**Done when:** you can break the function on purpose and the alarm fires.

## 6. Test data

Use a sandbox account and create varied identities with Terraform so the analysis has something to find:

- An admin role and a read-only role
- A role with a wildcard policy
- A role trusting another account
- An unused role
- A user with an old access key
- Roles with inline and managed policies, and one with a permission boundary

The 1000+ AWS managed policies already in the account give you real policy documents for free. CloudGoat is useful for risky scenarios but should only be deployed in a throwaway account.

## 7. Security checklist

- [ ] No `.tfstate`, `.tfvars`, or account IDs in the public repo
- [ ] S3 encryption, versioning, Block Public Access on all buckets
- [ ] Lambda role limited to the permissions listed in Phase 3
- [ ] OIDC trust policy restricted to repo and branch
- [ ] checkov and tflint pass in CI
- [ ] Log retention set (for example 30 days)
- [ ] Sample data in the public repo is sanitized

## 8. Common problems and fixes

| Problem | Fix |
| --- | --- |
| Lambda fails with a handler error | Binary must be named `bootstrap`, built for linux/arm64 |
| Policy JSON looks garbled | Policy documents are URL-encoded; decode them |
| IAM throttling | Use `GetAccountAuthorizationDetails` instead of per-role calls, add retries |
| Scheduler runs at the wrong hour | Set the schedule timezone explicitly to `Asia/Kolkata` |
| Duplicate data after re-run | Use deterministic partition keys and overwrite |
| Terraform state chicken-and-egg | Keep a separate bootstrap config applied once locally |
| Athena scans too much | Partition by `dt`, use Parquet, set workgroup scan limits |

## 9. Definition of done

1. `terraform apply` on a clean account builds the whole system with no manual console steps.
2. The job runs at 6 PM IST daily without intervention.
3. Each day produces a raw snapshot and curated Parquet tables.
4. Athena can answer history and change questions.
5. Risky permissions trigger an alert.
6. A merged PR deploys through CI/CD using OIDC only.
7. The README explains architecture, setup, and design decisions.

## 10. Stretch goals

- Multi-account collection with `sts:AssumeRole` from a central account and AWS Organizations
- IAM Identity Center users and permission sets via `identitystore` and `sso-admin`
- CloudTrail correlation to show who changed a policy
- A second collector (Kubernetes RBAC or GitHub) behind a shared `Collector` interface
- HR dataset join (for example a Mockaroo employee CSV) to find leavers who still have access
- Containerized version on ECS Fargate with ECR
- A small dashboard on top of Athena

## 11. Resume bullets this project supports

- Built a serverless Go pipeline that snapshots AWS IAM roles and policies daily into a partitioned S3 data lake queryable with Athena.
- Provisioned all infrastructure with modular Terraform and S3 remote state, deployed through GitHub Actions using OIDC with no static credentials.
- Implemented automated IAM risk detection and change diffing with CloudWatch and SNS alerting.
- Enforced least privilege, encryption, and IaC security scanning (tflint, checkov) in CI.

## 12. Suggested timeline

| Week | Focus |
| --- | --- |
| 1 | Phase 0 to 2: local collector and S3 storage |
| 2 | Phase 3: Terraform and scheduled Lambda |
| 3 | Phase 4 and start of 5: CI/CD and Parquet |
| 4 | Phase 5 and 6: Athena, analysis, alerts |
| 5 | Phase 7: hardening, docs, README, stretch goals |
