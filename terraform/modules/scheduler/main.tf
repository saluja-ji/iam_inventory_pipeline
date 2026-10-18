variable "name" {
  type = string
}

variable "lambda_arn" {
  type = string
}

variable "cron" {
  type    = string
  default = "cron(0 18 * * ? *)"
}

variable "timezone" {
  type    = string
  default = "Asia/Kolkata"
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "invoke" {
  name               = "${var.name}-scheduler-invoke"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

data "aws_iam_policy_document" "invoke" {
  statement {
    actions   = ["lambda:InvokeFunction"]
    resources = [var.lambda_arn]
  }
}

resource "aws_iam_role_policy" "invoke" {
  name   = "invoke-lambda"
  role   = aws_iam_role.invoke.id
  policy = data.aws_iam_policy_document.invoke.json
}

resource "aws_scheduler_schedule" "this" {
  name                         = var.name
  description                  = "Daily IAM inventory snapshot"
  schedule_expression          = var.cron
  schedule_expression_timezone = var.timezone
  state                        = "ENABLED"

  flexible_time_window {
    mode = "OFF"
  }

  target {
    arn      = var.lambda_arn
    role_arn = aws_iam_role.invoke.arn
    input    = jsonencode({ source = "eventbridge-scheduler" })

    retry_policy {
      maximum_retry_attempts       = 2
      maximum_event_age_in_seconds = 3600
    }
  }
}

output "schedule_arn" {
  value = aws_scheduler_schedule.this.arn
}
