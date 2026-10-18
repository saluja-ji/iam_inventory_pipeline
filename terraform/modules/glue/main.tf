variable "name" {
  description = "Prefix; Glue database name is derived from it (underscores only)."
  type        = string
}

variable "data_bucket_name" {
  type = string
}

variable "results_bucket_name" {
  type = string
}

variable "projection_start" {
  description = "First dt partition Athena should consider (yyyy-MM-dd)."
  type        = string
  default     = "2026-01-01"
}

variable "scan_limit_bytes" {
  description = "Per-query data scan cap for the workgroup (minimum allowed by AWS is 10 MB)."
  type        = number
  default     = 1073741824
}

locals {
  # Column lists are generated from the Go row structs (`make schema`), so they cannot drift.
  schemas       = jsondecode(file("${path.module}/schemas.json"))
  database_name = replace(var.name, "-", "_")
}

resource "aws_glue_catalog_database" "this" {
  name        = local.database_name
  description = "IAM inventory snapshots"
}

resource "aws_glue_catalog_table" "curated" {
  for_each      = local.schemas
  name          = each.key
  database_name = aws_glue_catalog_database.this.name
  table_type    = "EXTERNAL_TABLE"

  parameters = {
    EXTERNAL                      = "TRUE"
    classification                = "parquet"
    "projection.enabled"          = "true"
    "projection.dt.type"          = "date"
    "projection.dt.format"        = "yyyy-MM-dd"
    "projection.dt.range"         = "${var.projection_start},NOW"
    "projection.dt.interval"      = "1"
    "projection.dt.interval.unit" = "DAYS"
    "storage.location.template"   = "s3://${var.data_bucket_name}/curated/${each.key}/dt=$${dt}/"
  }

  partition_keys {
    name = "dt"
    type = "string"
  }

  storage_descriptor {
    location      = "s3://${var.data_bucket_name}/curated/${each.key}/"
    input_format  = "org.apache.hadoop.hive.ql.io.parquet.MapredParquetInputFormat"
    output_format = "org.apache.hadoop.hive.ql.io.parquet.MapredParquetOutputFormat"

    ser_de_info {
      serialization_library = "org.apache.hadoop.hive.ql.io.parquet.serde.ParquetHiveSerDe"
    }

    dynamic "columns" {
      for_each = each.value
      content {
        name = columns.value.name
        type = columns.value.type
      }
    }
  }
}

resource "aws_athena_workgroup" "this" {
  name          = var.name
  force_destroy = true

  configuration {
    enforce_workgroup_configuration    = true
    publish_cloudwatch_metrics_enabled = true
    bytes_scanned_cutoff_per_query     = var.scan_limit_bytes

    result_configuration {
      output_location = "s3://${var.results_bucket_name}/athena/"
      encryption_configuration {
        encryption_option = "SSE_S3"
      }
    }
  }
}

locals {
  today = "date_format(current_timestamp AT TIME ZONE 'Asia/Kolkata', '%Y-%m-%d')"
  named_queries = {
    "admin-roles-today"           = "SELECT role_name, policy_name FROM role_policy_attachments WHERE dt = ${local.today} AND policy_name = 'AdministratorAccess'"
    "roles-created-last-7-days"   = "SELECT role_name, create_date FROM roles WHERE dt = ${local.today} AND create_date > current_timestamp - interval '7' day ORDER BY create_date DESC"
    "findings-today"              = "SELECT severity, rule, resource_type, resource_name, detail FROM findings WHERE dt = ${local.today} ORDER BY severity, rule"
    "roles-added-since-yesterday" = <<-SQL
      SELECT t.role_name, t.arn FROM roles t
      LEFT JOIN roles y ON y.arn = t.arn AND y.dt = date_format(current_timestamp AT TIME ZONE 'Asia/Kolkata' - interval '1' day, '%Y-%m-%d')
      WHERE t.dt = ${local.today} AND y.arn IS NULL
    SQL
    "stale-access-keys"           = "SELECT user_name, access_key_id, age_days FROM access_keys WHERE dt = ${local.today} AND status = 'Active' AND age_days > 90 ORDER BY age_days DESC"
  }
}

resource "aws_athena_named_query" "this" {
  for_each  = local.named_queries
  name      = each.key
  workgroup = aws_athena_workgroup.this.id
  database  = aws_glue_catalog_database.this.name
  query     = each.value
}

output "database_name" {
  value = aws_glue_catalog_database.this.name
}

output "workgroup" {
  value = aws_athena_workgroup.this.name
}
