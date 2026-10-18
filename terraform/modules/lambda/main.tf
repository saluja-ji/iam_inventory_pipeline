variable "function_name" {
  type = string
}

variable "source_dir" {
  description = "Directory holding the `bootstrap` binary (and optional collector.yaml). Produced by `make package`."
  type        = string
}

variable "data_bucket_arn" {
  type = string
}

variable "data_bucket_name" {
  type = string
}

variable "sns_topic_arn" {
  type = string
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "memory_size" {
  type    = number
  default = 1024
}

variable "timeout" {
  type    = number
  default = 300
}

variable "alert_min_severity" {
  type    = string
  default = "high"
}

variable "adot_layer_arn" {
  description = "ARN of the AWS Distro for OpenTelemetry collector Lambda layer (arm64). Empty disables OTel export."
  type        = string
  default     = ""
}

variable "extra_environment" {
  type    = map(string)
  default = {}
}

locals {
  otel_enabled = var.adot_layer_arn != ""
  environment = merge(
    {
      DATA_BUCKET        = var.data_bucket_name
      SNS_TOPIC_ARN      = var.sns_topic_arn
      ALERT_MIN_SEVERITY = var.alert_min_severity
      OTEL_SERVICE_NAME  = "iam-inventory"
    },
    local.otel_enabled ? {
      OTEL_EXPORTER_OTLP_ENDPOINT         = "http://localhost:4318"
      OTEL_EXPORTER_OTLP_PROTOCOL         = "http/protobuf"
      OPENTELEMETRY_COLLECTOR_CONFIG_URI  = "/var/task/collector.yaml"
      OPENTELEMETRY_COLLECTOR_CONFIG_FILE = "/var/task/collector.yaml"
    } : {},
    var.extra_environment,
  )
}

data "archive_file" "zip" {
  type        = "zip"
  source_dir  = var.source_dir
  output_path = "${path.root}/.build/${var.function_name}.zip"
}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/aws/lambda/${var.function_name}"
  retention_in_days = var.log_retention_days
}

resource "aws_sqs_queue" "dlq" {
  name                      = "${var.function_name}-dlq"
  message_retention_seconds = 1209600
  sqs_managed_sse_enabled   = true
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "this" {
  name               = "${var.function_name}-exec"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

data "aws_iam_policy_document" "permissions" {
  statement {
    sid       = "ReadIamAccountDetails"
    actions   = ["iam:GetAccountAuthorizationDetails"]
    resources = ["*"] # the API does not support resource-level permissions
  }

  statement {
    sid       = "ReadUserAccessKeyMetadata"
    actions   = ["iam:ListAccessKeys"]
    resources = ["arn:aws:iam::*:user/*"]
  }

  statement {
    sid     = "DataBucketObjects"
    actions = ["s3:PutObject", "s3:GetObject"]
    resources = [
      "${var.data_bucket_arn}/raw/*",
      "${var.data_bucket_arn}/curated/*",
      "${var.data_bucket_arn}/findings/*",
      "${var.data_bucket_arn}/diff/*",
    ]
  }

  statement {
    # Without ListBucket, S3 answers a missing key with 403 instead of 404, breaking "no previous snapshot".
    sid       = "DataBucketList"
    actions   = ["s3:ListBucket"]
    resources = [var.data_bucket_arn]
  }

  statement {
    sid       = "Logs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.this.arn}:*"]
  }

  statement {
    sid       = "Alerts"
    actions   = ["sns:Publish"]
    resources = [var.sns_topic_arn]
  }

  statement {
    sid       = "DeadLetter"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.dlq.arn]
  }

  dynamic "statement" {
    for_each = local.otel_enabled ? [1] : []
    content {
      sid       = "XRayExport"
      actions   = ["xray:PutTraceSegments", "xray:PutTelemetryRecords"]
      resources = ["*"]
    }
  }

  dynamic "statement" {
    for_each = local.otel_enabled ? [1] : []
    content {
      # The collector's awsemf exporter writes OTel metrics into the function's own log group.
      sid       = "CollectorEmfLogs"
      actions   = ["logs:CreateLogGroup", "logs:DescribeLogGroups", "logs:DescribeLogStreams"]
      resources = [aws_cloudwatch_log_group.this.arn, "${aws_cloudwatch_log_group.this.arn}:*"]
    }
  }
}

resource "aws_iam_role_policy" "this" {
  name   = "least-privilege"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.permissions.json
}

resource "aws_lambda_function" "this" {
  function_name    = var.function_name
  role             = aws_iam_role.this.arn
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = data.archive_file.zip.output_path
  source_code_hash = data.archive_file.zip.output_base64sha256
  memory_size      = var.memory_size
  timeout          = var.timeout
  layers           = local.otel_enabled ? [var.adot_layer_arn] : []

  environment {
    variables = local.environment
  }

  dead_letter_config {
    target_arn = aws_sqs_queue.dlq.arn
  }

  tracing_config {
    mode = local.otel_enabled ? "Active" : "PassThrough"
  }

  depends_on = [aws_cloudwatch_log_group.this, aws_iam_role_policy.this]
}

output "function_name" {
  value = aws_lambda_function.this.function_name
}

output "function_arn" {
  value = aws_lambda_function.this.arn
}

output "role_arn" {
  value = aws_iam_role.this.arn
}

output "dlq_name" {
  value = aws_sqs_queue.dlq.name
}

output "dlq_arn" {
  value = aws_sqs_queue.dlq.arn
}

output "log_group_name" {
  value = aws_cloudwatch_log_group.this.name
}
