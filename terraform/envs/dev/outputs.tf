output "data_bucket" {
  value = module.data_bucket.id
}

output "lambda_name" {
  value = module.lambda.function_name
}

output "athena_workgroup" {
  value = module.glue.workgroup
}

output "glue_database" {
  value = module.glue.database_name
}

output "alerts_topic_arn" {
  value = aws_sns_topic.alerts.arn
}

output "github_plan_role_arn" {
  value = try(module.github_oidc[0].plan_role_arn, null)
}

output "github_apply_role_arn" {
  value = try(module.github_oidc[0].apply_role_arn, null)
}
