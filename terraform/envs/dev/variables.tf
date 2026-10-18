variable "region" {
  type    = string
  default = "ap-south-1"
}

variable "environment" {
  type    = string
  default = "dev"
}

variable "alert_email" {
  description = "Address that receives findings and alarm notifications (needs a confirmation click)."
  type        = string
}

variable "lambda_source_dir" {
  description = "Directory produced by `make package`."
  type        = string
  default     = "../../../build/lambda"
}

variable "adot_layer_arn" {
  description = "ADOT collector layer ARN for arm64 in this region. Leave empty to run without OTel export."
  type        = string
  default     = ""
}

variable "alert_min_severity" {
  type    = string
  default = "high"
}

variable "raw_retention_days" {
  type    = number
  default = 365
}

variable "log_retention_days" {
  type    = number
  default = 30
}

variable "github_repo" {
  description = "owner/repo for GitHub OIDC. Empty skips creating the OIDC roles."
  type        = string
  default     = ""
}

variable "create_github_oidc_provider" {
  type    = bool
  default = true
}

variable "state_bucket_name" {
  description = "Name of the bootstrap state bucket (used to scope the CI roles)."
  type        = string
  default     = ""
}

variable "athena_scan_limit_bytes" {
  type    = number
  default = 1073741824
}

variable "projection_start" {
  description = "First day Athena partition projection covers."
  type        = string
  default     = "2026-01-01"
}

variable "lambda_extra_environment" {
  description = "Extra Lambda environment variables, e.g. { MAX_KEY_AGE_DAYS = \"1\" } while testing."
  type        = map(string)
  default     = {}
}
