# What you need to do (and I could not)

Everything in the repo is written and tested offline. It has **never run against a real AWS account**, so expect to fix a small issue or two on first deploy. Work through the steps in order; each ends with a "you're done when" check.

Time: about 1.5 hours the first time. Cost: pennies per day (Lambda once a day, small S3, Athena only when you query). Remember to destroy the sandbox when finished (step 11).

---

## 0. Install the tools

You already have `go`, `terraform` and `aws`. Also install:

- `tflint` and `checkov` (`pipx install checkov`): CI runs them; run them locally first.
- `golangci-lint` built with a Go version at least as new as `go.mod` requires. The copy on this machine was built with Go 1.26, and `go.mod` needs 1.26.0 or newer, so run `golangci-lint --version` and upgrade if it complains.
- `zip` is not required; Terraform packages the Lambda itself.
- Optional, for local trace viewing: Docker.

**Done when:** `make test` passes and `make local-demo` prints spans.

## 1. Get a sandbox AWS account

1. Use a **dedicated sandbox account**, never one with production workloads. A fresh personal account is fine.
2. Turn on billing alerts: Billing > Budgets > create a monthly budget of about $5 with an email alert.
3. Create a human admin identity. Preferred: **IAM Identity Center** (`aws configure sso`). Acceptable for a sandbox: an IAM user with MFA and `aws configure`. Never use the root account for daily work.
4. Verify: `aws sts get-caller-identity` shows the sandbox account.

Region used throughout: `ap-south-1` (Mumbai). Change `region` in the tfvars if you prefer another.

**Done when:** `aws sts get-caller-identity` works.

## 2. Smoke-test the collector against real IAM (no deployment yet)

```bash
go run ./cmd/local -out ./out
```

This needs only `iam:GetAccountAuthorizationDetails` and `iam:ListAccessKeys`. Inspect `out/raw/dt=*/iam.json` and `out/findings/dt=*/findings.json`.

Things worth checking, since I could not: policy documents are valid JSON (URL-decoding worked), and the role/user counts match the console.

**Done when:** the run prints non-zero roles and the JSON looks right. Do **not** commit `out/` (it is git-ignored, and contains your account's real IAM data).

## 3. Bootstrap the Terraform state bucket (once, local state)

```bash
cd terraform/bootstrap
terraform init
terraform apply -var state_bucket_name=<globally-unique-name>   # e.g. saluja-ji-tfstate-<random>
terraform output -raw backend_hcl > ../envs/dev/backend.hcl
```

The state bucket is created with versioning, encryption, public access blocked and `prevent_destroy`. Its own state stays local in `terraform/bootstrap/terraform.tfstate` (git-ignored; keep a backup, it is tiny).

**Done when:** `terraform/envs/dev/backend.hcl` exists with your bucket name.

## 4. Deploy the stack

```bash
cp terraform/envs/dev/dev.tfvars.example terraform/envs/dev/dev.tfvars
# edit: alert_email, state_bucket_name; keep github_repo = "saluja-ji/iam_inventory_pipeline" (or "" to skip CI roles for now)

make package                      # builds build/lambda/{bootstrap,collector.yaml}
cd terraform/envs/dev
terraform init -backend-config=backend.hcl
terraform plan  -var-file=dev.tfvars
terraform apply -var-file=dev.tfvars
```

Then:

1. **Confirm the SNS subscription**: AWS emails `alert_email`; click "Confirm subscription". Until you do, no alerts arrive.
2. Note the outputs (`terraform output`).

Likely first-run snags: S3 bucket name taken (change `environment`), or the Glue/Athena resources complaining about the schema file (run `make schema`).

**Done when:** `terraform apply` succeeds and the subscription shows "Confirmed".

## 5. Run it once and query it

```bash
FN=$(terraform output -raw lambda_name)
aws lambda invoke --function-name "$FN" --cli-binary-format raw-in-base64-out --payload '{}' /dev/stdout
aws logs tail /aws/lambda/$FN --since 10m
BUCKET=$(terraform output -raw data_bucket); aws s3 ls s3://$BUCKET --recursive
```

Run the invoke **twice** and confirm S3 still has exactly one object per partition (idempotency).

Athena (console, workgroup `iam-inventory-dev`, database `iam_inventory_dev`): open **Saved queries** and run `admin-roles-today`, `findings-today`, etc. These use the current IST date, so run them the same day as an invoke.

**Done when:** logs show `run succeeded`, S3 has `raw/`, `curated/`, `findings/`, `diff/`, and a saved query returns rows.

If Athena returns nothing: check the partition exists (`aws s3 ls s3://$BUCKET/curated/roles/`) and that `projection_start` is earlier than today.

## 6. Turn on OpenTelemetry export (the new part of the project)

The Go code already creates spans and metrics and speaks OTLP. In Lambda, export needs the **AWS Distro for OpenTelemetry (ADOT) collector layer**.

1. Find the current **arm64** ADOT collector layer ARN for your region in the official docs: search for "AWS Distro for OpenTelemetry Lambda collector layer ARNs". Do not guess the version number; copy it from there. (It looks like `arn:aws:lambda:<region>:<publisher-account>:layer:aws-otel-collector-arm64-ver-<x-y-z>:<n>`.)
2. Put it in `dev.tfvars` as `adot_layer_arn = "..."` and `terraform apply -var-file=dev.tfvars` again. This also enables X-Ray active tracing and adds the extra IAM permissions.
3. Invoke the function again.
4. Verify:
   - **Traces:** CloudWatch > X-Ray traces (or Application Signals/Trace Map). You should see `pipeline.run` with children `collect`, `store.raw`, `analyze`, `store.curated`, `diff`, `notify`, plus a span per AWS API call.
   - **Logs correlate:** a log line in `/aws/lambda/iam-inventory-dev` contains `trace_id`.
   - **Metrics:** CloudWatch > Metrics > namespace `IAMInventory/OTel` (from OpenTelemetry) and `IAMInventory` (from EMF, drives alarms).

Things I could not verify and you may need to adjust in [otel/collector.yaml](../otel/collector.yaml): the layer's environment variable name (`OPENTELEMETRY_COLLECTOR_CONFIG_URI` on newer layers, `..._FILE` on older; both are set), and whether the `awsemf` exporter needs extra log permissions beyond those granted. If the collector fails to start, the function logs show the collector error. The function **still works** without export; telemetry is best-effort and a failed flush only logs a warning.

**No-AWS alternative to see traces right now (Jaeger):**

```bash
docker run --rm -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf \
  go run ./cmd/local -fixture testdata/sample_iam.json -out out
# open http://localhost:16686 and pick service iam-inventory-local
```

Take a screenshot of the trace for your portfolio.

**Done when:** you can see one end-to-end trace and a log line sharing its `trace_id`.

## 7. Create risky test identities and watch the alert fire

```bash
cd terraform/sandbox_identities
terraform init && terraform apply
```

Invoke the Lambda again. Within a minute you should get an email listing findings (admin attached, wildcard policy, broad PassRole, external trust, conditional-wildcard trust). Each identity's expected finding is in the comments of `main.tf`.

The `OLD_ACCESS_KEY` rule needs a key older than 90 days. To test it sooner: `aws iam create-access-key --user-name iaminv-test-user`, set `lambda_extra_environment = { MAX_KEY_AGE_DAYS = "1" }` in tfvars, apply, and invoke after a day. **Delete the key and never commit it.**

Then answer "what changed since yesterday?" with `diff/dt=*/diff.json` (it shows the new roles as added). You can also run the `roles-added-since-yesterday` saved query the next day.

**Done when:** you received the alert email. Take a screenshot (redact the account ID).

## 8. GitHub repo, OIDC and CI/CD

1. Create `github.com/saluja-ji/iam_inventory_pipeline` (public is fine; see the security checklist first). Make sure `git status` shows no `*.tfvars`, `backend.hcl`, `out/`, or state files, then push.
2. Make sure `github_repo` was set in step 4, so the OIDC provider and the two roles exist. Get the role ARNs: `terraform output github_plan_role_arn github_apply_role_arn`. If the account already had a GitHub OIDC provider, set `create_github_oidc_provider = false`.
3. Repo > Settings > Secrets and variables > Actions > **Variables** (not secrets; none of these are secret):

   | Variable | Value |
   | --- | --- |
   | `AWS_REGION` | `ap-south-1` |
   | `AWS_PLAN_ROLE_ARN` | plan role ARN |
   | `AWS_APPLY_ROLE_ARN` | apply role ARN |
   | `TF_STATE_BUCKET` | state bucket name |
   | `ALERT_EMAIL` | your email |
   | `ADOT_LAYER_ARN` | the layer ARN, or empty |

4. Repo > Settings > **Environments** > New environment `production`: add yourself as a **required reviewer** and restrict deployment to the `main` branch. This is the approval gate, and the apply role's trust policy only accepts tokens from this environment.
5. Settings > Branches: protect `main` (require PR and the `ci` checks).
6. Open a small PR. Expect `ci` and `plan` (with a plan comment). Merge it; `apply` waits for your approval, then deploys.

Notes: CI runs `checkov` and `tflint`; I could not run them here, so the first run may flag things. The `skip_check` list in `ci.yml` is a starting guess for accepted risks; run `checkov -d terraform` locally and keep only the skips you can justify (see [SECURITY.md](SECURITY.md)). The apply role is effectively admin (it must create IAM roles); its protection is the trust policy, not the permissions.

**Done when:** a merged PR deployed with no AWS keys stored anywhere in GitHub.

## 9. Prove the alarms work (Phase 7 "break it on purpose")

1. Set `lambda_extra_environment = { DATA_BUCKET = "does-not-exist" }`, apply, invoke. Expect: Lambda error, `...-lambda-errors` alarm and an email, a message in the DLQ after retries (async invokes retry twice), and a failure trace in X-Ray.
2. Revert and apply. The alarm returns to OK.
3. The `...-no-successful-run` alarm needs 26 hours without a success; you can lower `stale_after_hours` in the monitoring module temporarily if you want to see it fire.

**Done when:** you have seen the error alarm email once.

## 10. Make the schedule real

The scheduler runs daily at 18:00 Asia/Kolkata automatically after step 4. The next day, check `aws s3 ls s3://$BUCKET/raw/` shows a new `dt=` folder, and that day's partition appears in Athena without any crawler.

## 11. Clean up when you are done demoing

```bash
cd terraform/sandbox_identities && terraform destroy
cd ../envs/dev && terraform destroy -var-file=dev.tfvars
```

The data bucket is not `force_destroy`, so empty it first (versioned): easiest is the console "Empty" button. The state bucket has `prevent_destroy`; delete it by hand only when you are completely finished.

## 12. Before it goes on your resume

- Replace the `<N>` placeholders in the README resume bullets with real numbers from your account.
- Add screenshots: X-Ray trace, the alert email (redacted), an Athena result, the green pipeline.
- Re-read [SECURITY.md](SECURITY.md) and sanitize: the repo must contain no account IDs, real ARNs or IAM dumps. `testdata/sample_iam.json` is synthetic; keep it that way.
- Be ready to explain the decisions in the README's "Design decisions" section; interviewers ask about those, not about the file list.

## What I could not verify (honest list)

- `terraform plan/apply`: only `terraform validate` was run. No AWS calls of any kind were made.
- `tflint`, `checkov`, `golangci-lint`: not run locally (tools missing or built with an older Go).
- ADOT layer behaviour (env var names, awsemf permissions, X-Ray segment output).
- Athena reading the Parquet timestamp columns and partition projection: the standard configuration, but untested against real Athena.
- Real-world IAM edge cases (very large accounts, paging); unit tests use fakes.
