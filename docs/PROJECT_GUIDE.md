# Project Guide: understanding IAM Inventory from zero

Read this first. It explains what the project is, why it exists, how the pieces connect, and in what order to read the code. Other docs go deeper on one topic each; this one ties them together.

## 1. The project in one minute

**Problem.** In AWS, *IAM* decides who (users, roles) can do what. Over time it grows messy: someone attaches `AdministratorAccess` "temporarily", a role trusts an unknown account, an access key is never rotated. The AWS console only shows *now*, not history, and nobody checks it daily.

**Solution.** Every day at 6 PM India time, a small program:

1. **Photographs** the whole IAM configuration of the account (a *snapshot*).
2. **Saves** it in S3 in a form you can query with SQL, one folder per day, so you keep history.
3. **Checks** it against risk rules and emails you if something looks dangerous.
4. **Compares** it with the previous snapshot to say what changed.
5. **Reports on itself** (traces, metrics, logs, alarms) so you can tell when it is broken. This is the OpenTelemetry part.

Everything is created with code (Terraform), deployed by a pipeline (GitHub Actions), and runs without servers (AWS Lambda).

If you can say that paragraph back, you understand the project. The rest is detail.

## 2. Two stories to keep in mind

The project is really two systems that work together:

| | **The product** (what it does) | **The platform** (how it is built and run) |
| --- | --- | --- |
| What | The Go program: collect, analyze, store, diff, alert | Terraform, CI/CD, monitoring, security |
| Skills shown | Go, AWS SDK, data engineering, security analysis | DevOps, IaC, least privilege, observability |
| Where | `cmd/`, `internal/` | `terraform/`, `.github/`, `otel/` |

When you feel lost, ask: "am I looking at the product or the platform?"

## 3. A day in the life of one run

This is the most useful section. Follow one run, step by step, and note which file does each step.

```
18:00 IST  EventBridge Scheduler fires                     terraform/modules/scheduler
   |
   v
           Lambda starts, sets up telemetry and AWS clients cmd/lambda/main.go
   |
   v
 1 collect   ask IAM for everything (a few paged calls)    internal/collector
 2 store raw save the full snapshot as JSON                internal/pipeline -> internal/store
 3 analyze   run risk rules, produce "findings"            internal/analyze
 4 curate    flatten into 8 tables, write Parquet          internal/transform -> internal/store
 5 diff      load an earlier snapshot, compare             internal/analyze/diff.go
 6 notify    if findings are serious, email via SNS        internal/notify
   |
   v
           record metrics, flush telemetry, return         internal/telemetry
```

The conductor of steps 1 to 6 is one function: `Run` in [internal/pipeline/pipeline.go](../internal/pipeline/pipeline.go). Read it top to bottom once; it is the table of contents of the product.

### What is on disk after a run (on `2026-10-18`)

```
raw/dt=2026-10-18/iam.json                          everything, exactly as collected
curated/roles/dt=2026-10-18/roles.parquet           one file per table, same for users, groups,
curated/findings/dt=2026-10-18/findings.parquet     policies, role_policy_attachments,
...                                                 trust_relationships, access_keys, findings
findings/dt=2026-10-18/findings.json                the same findings, readable by humans
diff/dt=2026-10-18/diff.json                        added / removed / modified since last time
```

`dt=2026-10-18` is the **partition**. Because every day lives in its own folder, a query for one day reads only that day's files, which is cheap and fast. Running twice on the same day overwrites the same keys, so there are never duplicates.

## 4. Concepts you need (and nothing more)

Skip any you already know.

- **IAM.** AWS's permission system. *Users* are people or programs with long-lived credentials; *roles* are identities that something temporarily assumes; *groups* bundle users; *policies* are JSON documents listing allowed actions. A role also has a **trust policy**: who is allowed to assume it. Many real breaches are a too-trusting trust policy or an over-broad policy.
- **Snapshot.** One full copy of all that, taken at a moment in time. History is just many snapshots.
- **Lambda.** Run a function on demand without managing a server. You pay per run. Ours runs once a day for a few seconds.
- **EventBridge Scheduler.** Cron for AWS. It supports time zones, which is why "6 PM IST" is exact.
- **S3.** Object storage. "Folders" are just key prefixes, which is what makes the `dt=` trick work.
- **Parquet.** A column-oriented file format. Much smaller and faster to query than JSON. Raw JSON is kept for fidelity; Parquet is kept for querying.
- **Glue Data Catalog + Athena.** Glue stores table definitions (names and columns). Athena runs SQL directly over files in S3. Together they turn the folders above into tables. **Partition projection** tells Athena "a `dt` folder exists for every date", so no crawler is needed.
- **Terraform.** Describe infrastructure in files; `terraform apply` makes AWS match. Nothing is clicked in the console.
- **OIDC for GitHub Actions.** Lets CI prove who it is to AWS with a short-lived token, so no AWS password is stored in GitHub.
- **OpenTelemetry (OTel).** A vendor-neutral standard for telemetry. A **trace** is the story of one run as nested timed steps ("spans"). **Metrics** are numbers over time. **Logs** are text lines. OTel's useful trick is linking them: each log line carries the `trace_id` of the step that wrote it.
- **EMF (CloudWatch Embedded Metric Format).** A log line shaped so CloudWatch turns it into a metric automatically. We use it so alarms work even if OTel export is off.

## 5. How the code is organized, and why

The product code is small, one idea per package. Dependencies point inward: `pipeline` knows everyone, the leaf packages know little.

| Package | One-line job | Worth noticing |
| --- | --- | --- |
| [model](../internal/model/model.go) | The shared vocabulary: `Snapshot`, `Role`, `User`, `Group`, `Policy` | Start here. Everything else passes these around. |
| [policydoc](../internal/policydoc/policydoc.go) | Parse IAM policy JSON | IAM JSON is inconsistent (a field may be a string *or* a list); this hides that. |
| [collector](../internal/collector/collector.go) | Read IAM into a `Snapshot` | Uses an interface (`IAMAPI`) so tests use a fake instead of AWS. URL-decodes policy documents. |
| [analyze](../internal/analyze/analyze.go) | Risk rules, and the diff | Rules are plain functions over the model; add a rule by adding a case. |
| [transform](../internal/transform/transform.go) | Snapshot to flat rows to Parquet | Row structs double as the table schema (see `schema.go`). |
| [store](../internal/store/store.go) | Put/Get objects by key | Two backends: S3 and a local directory. The local one is why everything is testable offline. |
| [notify](../internal/notify/notify.go) | Send the alert | SNS, or a no-op. |
| [telemetry](../internal/telemetry/telemetry.go) | OTel setup, log correlation, metrics | The only place that knows about exporters. |
| [pipeline](../internal/pipeline/pipeline.go) | Orchestrates all of the above | Also where every run's metrics are recorded, success or failure. |

Two design habits repeat everywhere and are worth learning from:

1. **Interfaces at the edges** (`IAMAPI`, `Backend`, `Source`, `Notifier`). Real AWS in production, fakes in tests, local files in the demo.
2. **Deterministic keys.** The output location is a pure function of the date, which gives idempotency for free.

### Two entry points, one pipeline

- [cmd/lambda](../cmd/lambda/main.go) is the production entry. It wires real AWS clients and starts Lambda.
- [cmd/local](../cmd/local/main.go) is the same pipeline on your laptop. With `-fixture testdata/sample_iam.json` it needs no AWS at all. This is your best learning tool; see section 8.

## 6. The platform: Terraform, CI/CD, monitoring

All under [terraform/](../terraform). Read modules in the order data flows:

| Module | Creates | Connects to |
| --- | --- | --- |
| `bootstrap/` | The bucket that stores Terraform's own state | Run once by hand; everything else depends on it |
| `modules/s3` | A locked-down bucket (encrypted, private, versioned, TLS-only, lifecycle rules) | Used twice: data bucket and Athena results bucket |
| `modules/lambda` | The function, its log group, a dead-letter queue, and a **least-privilege role** | Read its `permissions` block: every line maps to something the code does |
| `modules/scheduler` | The 6 PM IST trigger and the role allowed to invoke Lambda | Points at the Lambda |
| `modules/glue` | Database, 8 tables, Athena workgroup with a scan limit, saved example queries | Reads `schemas.json`, generated from the Go structs |
| `modules/monitoring` | 3 alarms and a dashboard | Watches the Lambda, the DLQ and the `run_success` metric |
| `modules/github_oidc` | The trust relationship that lets CI deploy without keys | Used by the workflows |
| `envs/dev` | **Wires all modules together** | Start here to see the whole system on one page |
| `sandbox_identities` | Intentionally risky test roles | For testing the analyzer; sandbox accounts only |

CI/CD lives in [.github/workflows](../.github/workflows):

- `ci.yml` on every PR: tests, lint, `terraform fmt/validate`, tflint, checkov.
- `plan.yml` on PRs: shows what Terraform *would* change, as a PR comment.
- `apply.yml` on merge to `main`: applies, but only after a human approves the `production` environment.

## 7. Where OpenTelemetry fits

Think of it as the project's black-box recorder.

```
Go code (spans, metrics)
   | OTLP over localhost
   v
ADOT collector (a Lambda "layer" running beside your code)   config: otel/collector.yaml
   |                          |
   v                          v
X-Ray (traces)         CloudWatch (metrics)
```

- **In the code:** [internal/telemetry](../internal/telemetry) sets it up; each pipeline stage opens a span; the AWS SDK is wrapped so each AWS call becomes a span too.
- **In a run:** open one trace and you see `pipeline.run` broken into `collect`, `store.raw`, `analyze`... with durations. If a run is slow or fails, you see which stage.
- **Log linkage:** every log line includes `trace_id`. Find a failing log, paste its `trace_id`, see the whole run.
- **Why a collector instead of exporting directly:** the app only speaks OTLP, so switching from X-Ray to Grafana or Jaeger changes a YAML file, not Go code.
- **Why also EMF:** alarms must not depend on an optional component. Details in [OBSERVABILITY.md](OBSERVABILITY.md).

Without the collector layer configured, the project still works; spans are created but not exported.

## 8. Learn by doing: a guided path

Do these in order. Each takes 10 to 20 minutes and needs no AWS account.

1. **Run it.** `make local-demo`. You will see span output (the trace) and a summary line. Then `find out -type f` to see the day's files, and open `out/findings/dt=*/findings.json`.
2. **Read the input.** Open [testdata/sample_iam.json](../testdata/sample_iam.json). Find `wildcard-role` and `cross-account-role`. Predict which findings they will produce, then check your answer in the findings file.
3. **Read the conductor.** Open `pipeline.go`, function `Run`. Match each step to the list in section 3.
4. **Change a rule.** In [analyze.go](../internal/analyze/analyze.go), change the `MaxKeyAgeDays` passed in `TestEvaluateRules` (it defaults to 90) and see how the `OLD_ACCESS_KEY` expectation changes. Run `go test ./internal/analyze`. Then add a brand-new rule, for example "role has no permissions boundary", with a test.
5. **Watch idempotency.** Run `make local-demo` twice and count the files. Same number.
6. **See a diff.** Read `TestRunEndToEndAndIdempotent` in `internal/pipeline/pipeline_test.go`: it runs day 1 twice, then day 2, and checks that the second day finds the earlier snapshot. Then inspect `diff.json` from `make local-demo` (it says `baseline: true` because there is no earlier day).
7. **Add a column.** Add a field to `RoleRow` in [transform.go](../internal/transform/transform.go), run `go test ./...` (a test fails because the Glue schema is stale), run `make schema`, tests pass. This shows how Go and Terraform stay in sync.
8. **Break it.** Make `Collect` return an error and see the run fail, the failure logged, and `"run_success":0` emitted.
9. **View a trace in a UI.** Run Jaeger in Docker and follow the snippet in [USER_ACTIONS.md](USER_ACTIONS.md) step 6.

After this you will understand the product. Then read `terraform/envs/dev/main.tf` to see how it is hosted.

## 9. Question to file map

| If you wonder... | Look at |
| --- | --- |
| What does the schedule look like, and why IST? | `terraform/modules/scheduler/main.tf`, `internal/pipeline` (`IST`) |
| What exactly can the Lambda do in AWS? | `terraform/modules/lambda/main.tf`, `data "aws_iam_policy_document" "permissions"` |
| What counts as "risky"? | `internal/analyze/analyze.go`, rule constants at the top |
| How is "what changed" computed? | `internal/analyze/diff.go` (hash each identity, compare by ARN) |
| Why is my Athena table empty? | `docs/RUNBOOK.md`, "Athena returns no rows" |
| How do I add a new table? | `internal/transform` (row struct + `Build`), then `make schema` |
| How does CI get AWS access without a key? | `terraform/modules/github_oidc`, `.github/workflows/plan.yml` |
| How do I know a run failed? | `terraform/modules/monitoring`, `docs/OBSERVABILITY.md` |
| Something broke in production | `docs/RUNBOOK.md` |
| I need to deploy it | `docs/USER_ACTIONS.md` |

## 10. Common misunderstandings

- **"It stores the IAM data in a database."** No database. Files in S3 are the source of truth; Athena queries them in place.
- **"Parquet replaces the JSON."** Both are kept. JSON is the faithful archive and the input for tomorrow's diff; Parquet is for SQL.
- **"The alarms use the OTel metrics."** They use the EMF metrics on purpose (section 7).
- **"The CI role is least privilege."** The *Lambda* role is. The *apply* role is effectively admin because it creates IAM roles; it is protected by who can assume it. See [SECURITY.md](SECURITY.md).
- **"`dt` is the UTC date."** It is the IST date, so a run at 18:00 IST and a query "today" in India agree.
- **"Each AWS role is fetched with its own API call."** One paged call returns everything; that is deliberate, to avoid throttling.

## 11. The document map

| Doc | Read it when |
| --- | --- |
| This guide | You are new |
| [README.md](../README.md) | You want the pitch, diagram, design decisions, resume bullets |
| [USER_ACTIONS.md](USER_ACTIONS.md) | You are ready to deploy to a real AWS account |
| [OBSERVABILITY.md](OBSERVABILITY.md) | You want the telemetry details and alarm definitions |
| [RUNBOOK.md](RUNBOOK.md) | An alarm fired |
| [SECURITY.md](SECURITY.md) | Before making the repo public |
| [../AWS IAM Inventory Pipeline Project Plan.md](../AWS%20IAM%20Inventory%20Pipeline%20Project%20Plan.md) | You want the original plan this was built from |

## 12. Explaining it in an interview (30 seconds)

"It's a serverless Go pipeline. A scheduled Lambda snapshots our AWS IAM every evening, writes it to an S3 data lake partitioned by date, and makes it queryable in Athena. It runs risk rules and a day-over-day diff and emails alerts. The infrastructure is modular Terraform, deployed through GitHub Actions with OIDC so there are no stored keys, and the service is instrumented with OpenTelemetry, so any run is a trace I can follow and the alarms tell me if it stops working."

Then be ready for the follow-ups the README's *Design decisions* section answers: why one API call, why Parquet and JSON, why two metric paths, why the apply role is admin.
