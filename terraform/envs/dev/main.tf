data "aws_caller_identity" "current" {}

locals {
  name       = "iam-inventory-${var.environment}"
  account_id = data.aws_caller_identity.current.account_id
}

module "data_bucket" {
  source      = "../../modules/s3"
  bucket_name = "${local.name}-${local.account_id}-data"
  expiry_rules = [
    { id = "expire-raw", prefix = "raw/", expire_in = var.raw_retention_days },
  ]
}

module "athena_results_bucket" {
  source        = "../../modules/s3"
  bucket_name   = "${local.name}-${local.account_id}-athena"
  force_destroy = true
  expiry_rules = [
    { id = "expire-results", prefix = "athena/", expire_in = 30 },
  ]
}

resource "aws_sns_topic" "alerts" {
  #checkov:skip=CKV_AWS_26:CloudWatch alarms cannot publish to a topic encrypted with the AWS-managed SNS key; a CMK is a stretch goal.
  name = "${local.name}-alerts"
}

resource "aws_sns_topic_subscription" "email" {
  topic_arn = aws_sns_topic.alerts.arn
  protocol  = "email"
  endpoint  = var.alert_email
}

module "lambda" {
  source             = "../../modules/lambda"
  function_name      = local.name
  source_dir         = var.lambda_source_dir
  data_bucket_arn    = module.data_bucket.arn
  data_bucket_name   = module.data_bucket.id
  sns_topic_arn      = aws_sns_topic.alerts.arn
  log_retention_days = var.log_retention_days
  alert_min_severity = var.alert_min_severity
  adot_layer_arn     = var.adot_layer_arn
  extra_environment  = var.lambda_extra_environment
}

module "scheduler" {
  source     = "../../modules/scheduler"
  name       = local.name
  lambda_arn = module.lambda.function_arn
  # 6 PM India Standard Time, every day.
  cron     = "cron(0 18 * * ? *)"
  timezone = "Asia/Kolkata"
}

module "glue" {
  source              = "../../modules/glue"
  name                = local.name
  data_bucket_name    = module.data_bucket.id
  results_bucket_name = module.athena_results_bucket.id
  projection_start    = var.projection_start
  scan_limit_bytes    = var.athena_scan_limit_bytes
}

module "monitoring" {
  source        = "../../modules/monitoring"
  name          = local.name
  function_name = module.lambda.function_name
  dlq_name      = module.lambda.dlq_name
  sns_topic_arn = aws_sns_topic.alerts.arn
}

module "github_oidc" {
  count                = var.github_repo == "" ? 0 : 1
  source               = "../../modules/github_oidc"
  name                 = local.name
  github_repo          = var.github_repo
  state_bucket_name    = var.state_bucket_name
  create_oidc_provider = var.create_github_oidc_provider
}
