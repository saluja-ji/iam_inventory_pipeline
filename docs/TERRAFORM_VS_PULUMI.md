# Terraform vs Pulumi, and which fits this project

The infrastructure for this pipeline now exists twice: the original Terraform in [terraform/](../terraform) and a Pulumi (Go) port in [pulumi/](../pulumi). Both describe the same AWS resources. This document explains how the two tools differ in general, then which one suits **this** project and why.

## TL;DR

**For this project, Pulumi (in Go) is the better fit, by a moderate margin, not a landslide.**

- The Lambda, the Parquet schemas and the tests are all Go. Pulumi lets the infrastructure be Go too, so Glue table columns come straight from the `transform` row structs. Terraform needs a generator (`make schema` → `schemas.json`) plus a CI check to stop drift. Pulumi needs neither.
- The infrastructure can be unit-tested offline with the same `go test` the rest of the repo uses. [pulumi/components/components_test.go](../pulumi/components/components_test.go) checks that the bucket policy denies non-TLS, that X-Ray permissions exist only when the ADOT layer is set, that the OIDC trust policies are scoped per role, and so on, with no AWS account.
- Nothing is deployed yet ([README](../README.md) status), so there is no state to migrate. Switching tools now is as cheap as it will ever be.

Terraform still wins on ecosystem maturity, hiring pool, and the readability of its plans. If the team already runs Terraform everywhere, or this repo needs to match an existing Terraform estate, that outweighs the points above. See [When Terraform is the better choice](#when-terraform-is-the-better-choice).

## How the tools differ

| Area | Terraform | Pulumi |
| --- | --- | --- |
| **Language** | HCL, a declarative DSL. Loops are `for_each`/`count`/`dynamic` blocks; logic is expressions and functions. | General-purpose languages (Go, TypeScript, Python, C#, Java, YAML). Real loops, functions, types, packages. |
| **Reuse** | Modules (directory + variables + outputs). Shared via registry or git. | Component resources (classes/structs) shared as ordinary packages (Go modules, npm, PyPI). |
| **State** | State file in a backend (S3, etc.). S3 backend has native locking since 1.10 (`use_lockfile`). | State in Pulumi Cloud (default) or a self-managed backend (S3, local file). Self-managed S3 works, with a lock mechanism, but it is a "DIY backend" with fewer features than Pulumi Cloud. |
| **Secrets** | Values are plain in state unless the provider marks them sensitive; state must be protected. | Config and state values can be encrypted per stack (passphrase, KMS, or Pulumi Cloud) and secrets propagate through outputs. |
| **Testing** | `terraform validate`, `terraform test` (HCL tests, with mock providers since 1.7), tflint, checkov. | Native unit tests with mocks in your language's test framework, plus integration tests and policy packs. |
| **Preview** | `terraform plan`, widely understood, stable output format. | `pulumi preview`, similar, also shows resource hierarchy via components. |
| **Providers** | Largest ecosystem, most providers first. | Most providers are bridged from Terraform providers, so coverage is close but the SDK lags slightly. For AWS the resource surface is the same. |
| **Type safety / IDE** | Limited: editor plugins, `validate`. Typos in attribute names surface at validate/plan. | Compiler and IDE autocomplete catch wrong fields, wrong types and missing args before any plan. |
| **Dynamic data** | `data` sources, `locals`, functions (`jsondecode`, `file`). Wiring values between resources is implicit via references. | Same concept with `Output[T]`; transforming values needs `.ApplyT`/`pulumi.Sprintf`, which is the main learning curve. |
| **Policy / compliance** | Sentinel (paid), OPA, checkov, tflint. | CrossGuard policy packs (in code), also checkov-style scanners work on plans. |
| **Ecosystem / community** | Larger: more examples, modules, Stack Overflow answers, engineers who know it. Licence is BUSL since 2023 (OpenTofu is the open fork). | Smaller but active. Core is Apache 2.0. |
| **Cost** | Free CLI; HCP Terraform paid beyond free tier. | Free CLI; Pulumi Cloud free for individuals, paid for teams; S3 backend is free. |
| **Drift detection** | `plan` shows drift; refresh on plan. | `pulumi refresh` / `preview --refresh`; same idea. |
| **Import existing** | `terraform import`, `import` blocks. | `pulumi import`, plus a converter from Terraform HCL. |

## How that plays out in this repo

| Concern | Terraform version | Pulumi version |
| --- | --- | --- |
| Glue table columns | `make schema` runs [cmd/schemagen](../cmd/schemagen/main.go), commits `schemas.json`, and CI fails on drift (`git diff --exit-code`). | [pulumi/components/glue.go](../pulumi/components/glue.go) calls `transform.Schemas()` directly. Changing a row struct changes the table on the next `preview`. There is nothing to regenerate or forget. |
| Bucket hardening | Two copies of the "versioning + SSE + public-access-block + TLS-only policy" block: `modules/s3` and `bootstrap`. | One `Bucket` component used by the data bucket, Athena bucket **and** the state bucket in [bootstrap](../pulumi/bootstrap/main.go). |
| Optional OTel wiring | `dynamic "statement"` blocks, `merge()` of conditional maps, `count` on the OIDC module. | Plain `if otelEnabled { ... }` and `if githubRepo != ""`. |
| Policy documents | `aws_iam_policy_document` data sources. | JSON built from typed maps. Slightly less AWS-aware than the data source, but needs no extra resource type. |
| Tests | `terraform validate` + tflint + checkov: syntax and static rules, nothing that asserts behaviour. | Those static checks still apply conceptually, plus 4 behavioural unit tests that run in about 0.1 s. |
| Naming of the account in bucket names | `data.aws_caller_identity` | `aws.GetCallerIdentityOutput`, same thing. |
| Lambda zip | `archive_file` data source and `source_code_hash`. | `pulumi.NewFileArchive(dir)`; Pulumi zips and hashes it. One fewer provider (`archive`) to pin. |
| CI approval gate | OIDC roles + GitHub Environment. | Identical: the roles are the same, only the state-bucket policy and the commands changed. |
| Remote state | `backend "s3"` block + `backend.hcl`. | `pulumi login s3://<bucket>`. Bootstrap still has to create that bucket first, same chicken-and-egg. |
| Size | About 1,280 lines of HCL. | About 1,200 lines of Go, excluding tests. Go is wordier per resource, so do not expect a shrink. |

### Where Pulumi is worse here

- **Verbosity.** Go's typed args (`&s3.BucketLifecycleConfigurationRuleArgs{...}`) are longer than the equivalent HCL blocks. A reviewer who only reads infra will find the HCL quicker to scan.
- **Output plumbing.** Anything derived from another resource's attribute goes through `pulumi.Sprintf` / `.ApplyT`. Easy to misuse; the compiler cannot tell you that you passed an unresolved function value to `Sprintf` (that bug was made and caught while writing this port, which is exactly why the mock tests exist).
- **Secrets passphrase in CI.** With a self-managed S3 backend, someone has to own `PULUMI_CONFIG_PASSPHRASE` (or a KMS key). Terraform has no equivalent step. Pulumi Cloud removes it, at the price of a SaaS dependency.
- **The plan role needs more care.** The read-only PR role must also read/write the state bucket (already granted in the OIDC component) and be able to use the secrets provider.
- **Tooling.** tflint and checkov have no direct Pulumi equivalents in this repo. The port relies on the compiler, `go vet`, mock tests and (optionally) CrossGuard.
- **Fewer people know it.** Terraform skills are easier to hire for and easier to Google.

### Where Pulumi is better here

- **One language and one toolchain.** `go test`, `go vet`, gofmt and the same editor setup cover app and infra. The CI job is four lines.
- **No generated artifacts.** The schema-drift class of bug disappears.
- **Real unit tests.** Behaviour such as "no X-Ray permissions without the ADOT layer" is asserted, not just trusted.
- **Better abstractions.** The `Bucket` component is reused three times instead of copy-pasted; conditional logic is ordinary code.
- **Smaller dependency surface.** No `archive` provider, no separate HCL toolchain (terraform, tflint, checkov) for contributors who only write Go.

## When Terraform is the better choice

Choose Terraform (or OpenTofu) instead if any of these are true:

- Your organisation already standardises on Terraform and other teams must read, review or reuse this code.
- You rely on a Terraform-only module or provider that has no Pulumi equivalent.
- You want the largest possible hiring pool and community help.
- You are happy with a static, reviewable DSL and the infra is expected to stay small and rarely change. This project's infra is small and stable, so Terraform's costs are low too, which is why the margin here is moderate.

## Verification status

The Pulumi port was **built and tested offline only**:

- `go build ./...`, `go vet ./...` and `go test ./...` in `pulumi/` pass (mocked resource tests).
- It has **not** been run through `pulumi preview` or `pulumi up`, because the Pulumi CLI is not installed in this environment and no AWS credentials exist. Run `pulumi preview` once and compare it with `terraform plan` before trusting it. Expect Pulumi to show the Glue/Athena/CloudWatch resources as new.
- Resource names in AWS are pinned explicitly (`Name:` fields) so they match the Terraform ones. Pulumi's logical names (what shows in `preview`) are different from Terraform addresses, so **do not point both tools at the same live account**. They would each try to create the same named resources.

See [PULUMI.md](PULUMI.md) for how to deploy it.
