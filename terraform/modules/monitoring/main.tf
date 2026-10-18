variable "name" {
  type = string
}

variable "function_name" {
  type = string
}

variable "dlq_name" {
  type = string
}

variable "sns_topic_arn" {
  type = string
}

variable "metric_namespace" {
  type    = string
  default = "IAMInventory"
}

variable "service_dimension" {
  type    = string
  default = "iam-inventory"
}

variable "stale_after_hours" {
  type    = number
  default = 26
}

resource "aws_cloudwatch_metric_alarm" "errors" {
  alarm_name          = "${var.name}-lambda-errors"
  alarm_description   = "The IAM inventory Lambda raised an error."
  namespace           = "AWS/Lambda"
  metric_name         = "Errors"
  dimensions          = { FunctionName = var.function_name }
  statistic           = "Sum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [var.sns_topic_arn]
  ok_actions          = [var.sns_topic_arn]
}

# run_success is emitted by the function itself (CloudWatch EMF): 1 per good run, 0 per failed run.
# Hourly buckets with no data count as breaching, so N consecutive hours without a success trips it.
resource "aws_cloudwatch_metric_alarm" "no_recent_success" {
  alarm_name          = "${var.name}-no-successful-run"
  alarm_description   = "No successful run in ${var.stale_after_hours} hours."
  namespace           = var.metric_namespace
  metric_name         = "run_success"
  dimensions          = { Service = var.service_dimension }
  statistic           = "Sum"
  period              = 3600
  evaluation_periods  = var.stale_after_hours
  datapoints_to_alarm = var.stale_after_hours
  threshold           = 1
  comparison_operator = "LessThanThreshold"
  treat_missing_data  = "breaching"
  alarm_actions       = [var.sns_topic_arn]
  ok_actions          = [var.sns_topic_arn]
}

resource "aws_cloudwatch_metric_alarm" "dlq_depth" {
  alarm_name          = "${var.name}-dlq-not-empty"
  alarm_description   = "A failed invocation landed in the dead-letter queue."
  namespace           = "AWS/SQS"
  metric_name         = "ApproximateNumberOfMessagesVisible"
  dimensions          = { QueueName = var.dlq_name }
  statistic           = "Maximum"
  period              = 300
  evaluation_periods  = 1
  threshold           = 0
  comparison_operator = "GreaterThanThreshold"
  treat_missing_data  = "notBreaching"
  alarm_actions       = [var.sns_topic_arn]
}

resource "aws_cloudwatch_dashboard" "this" {
  dashboard_name = var.name
  dashboard_body = jsonencode({
    widgets = [
      {
        type = "metric", x = 0, y = 0, width = 12, height = 6
        properties = {
          title  = "Inventory size"
          region = data.aws_region.current.region
          stat   = "Maximum"
          period = 86400
          metrics = [
            [var.metric_namespace, "roles_collected", "Service", var.service_dimension],
            [var.metric_namespace, "users_collected", "Service", var.service_dimension],
            [var.metric_namespace, "groups_collected", "Service", var.service_dimension],
            [var.metric_namespace, "policies_collected", "Service", var.service_dimension],
          ]
        }
      },
      {
        type = "metric", x = 12, y = 0, width = 12, height = 6
        properties = {
          title  = "Findings and changes"
          region = data.aws_region.current.region
          stat   = "Maximum"
          period = 86400
          metrics = [
            [var.metric_namespace, "findings_count", "Service", var.service_dimension],
            [var.metric_namespace, "critical_high_findings", "Service", var.service_dimension],
            [var.metric_namespace, "changes_count", "Service", var.service_dimension],
          ]
        }
      },
      {
        type = "metric", x = 0, y = 6, width = 24, height = 6
        properties = {
          title  = "Run health"
          region = data.aws_region.current.region
          stat   = "Sum"
          period = 3600
          metrics = [
            [var.metric_namespace, "run_success", "Service", var.service_dimension],
            ["AWS/Lambda", "Errors", "FunctionName", var.function_name],
          ]
        }
      },
    ]
  })
}

data "aws_region" "current" {}

output "dashboard_name" {
  value = aws_cloudwatch_dashboard.this.dashboard_name
}
