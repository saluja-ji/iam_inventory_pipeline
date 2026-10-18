# Runbook

Start with the alert email or alarm name, then the matching section.

## `...-lambda-errors` or `...-dlq-not-empty`

1. Find the failure: `aws logs tail /aws/lambda/iam-inventory-dev --since 1h | grep '"msg":"run failed"'` (or the Logs Insights query in [OBSERVABILITY.md](OBSERVABILITY.md)).
2. Match the `error`:

   | Error contains | Cause | Fix |
   | --- | --- | --- |
   | `get account authorization details` and `Throttl` | IAM rate limit even after 8 attempts | re-invoke; if frequent, check for other tools hammering IAM |
   | `AccessDenied` on `iam:` | function role lost a permission | `terraform apply` to restore the role policy |
   | `put s3://` / `get s3://` AccessDenied | bucket policy or role change | check `DATA_BUCKET` env var and the role's S3 statements |
   | `sns publish` | topic deleted or permission lost | data is already stored; fix and re-invoke (re-run is idempotent) |
   | `Task timed out` | account unusually large | raise `timeout` / `memory_size` in the Lambda module |
   | `exec format error`, `Runtime.InvalidEntrypoint` | wrong binary | rebuild with `make package` (linux/arm64, file named `bootstrap`) |
3. Re-run: `aws lambda invoke --function-name iam-inventory-dev /dev/stdout`. Same-day re-runs overwrite, so it is always safe.
4. Purge the DLQ after fixing: `aws sqs purge-queue --queue-url <dlq url>`.
5. Use the failing run's `trace_id` in X-Ray to see which stage failed and how long each took.

## `...-no-successful-run`

No run succeeded for 26 hours.

1. Did it run at all? `aws logs filter-log-events --log-group-name /aws/lambda/iam-inventory-dev --start-time $(date -d '-2 days' +%s000) --filter-pattern run`.
2. No invocations: check the schedule is `ENABLED` (`aws scheduler get-schedule --name iam-inventory-dev`), its timezone is `Asia/Kolkata`, and the scheduler role can invoke the function.
3. Invocations but failures: previous section.
4. Only the metric is missing: check the log line `{"_aws":...}` is present; EMF is dropped if the JSON is malformed.

## Athena returns no rows

- Query the same IST date that has data: `aws s3 ls s3://$BUCKET/curated/roles/`.
- `projection_start` must be on or before the earliest partition.
- Scan limit hit: the error says the query exceeded the workgroup limit; always filter on `dt`.

## Backfilling or replaying

The pipeline always snapshots *now*. Past days cannot be recreated from IAM, but you can regenerate curated tables from a stored raw file by running `go run ./cmd/local -fixture <downloaded iam.json> -out out` and uploading the result.

## Rotating or removing the alert email

Change `alert_email`, apply, confirm the new subscription. Old subscription is removed by Terraform.
