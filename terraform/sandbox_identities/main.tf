# Deliberately risky identities so the analyzer has something to find.
# SANDBOX ACCOUNTS ONLY. Apply separately from envs/dev and destroy when done.
terraform {
  required_version = ">= 1.10"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = var.region
  default_tags {
    tags = { Project = "iam-inventory", Purpose = "analyzer-test-data", ManagedBy = "terraform" }
  }
}

variable "region" {
  type    = string
  default = "ap-south-1"
}

variable "external_account_id" {
  description = "Any other 12-digit account id to trust (nobody needs to own it)."
  type        = string
  default     = "999999999999"
}

data "aws_caller_identity" "current" {}

data "aws_iam_policy_document" "trust_lambda" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

# Expect: ADMIN_POLICY_ATTACHED
resource "aws_iam_role" "test_admin" {
  name               = "iaminv-test-admin"
  assume_role_policy = data.aws_iam_policy_document.trust_lambda.json
}

resource "aws_iam_role_policy_attachment" "test_admin" {
  role       = aws_iam_role.test_admin.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

# Expect: no findings
resource "aws_iam_role" "test_readonly" {
  name               = "iaminv-test-readonly"
  assume_role_policy = data.aws_iam_policy_document.trust_lambda.json
}

resource "aws_iam_role_policy_attachment" "test_readonly" {
  role       = aws_iam_role.test_readonly.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

# Expect: WILDCARD_ADMIN
resource "aws_iam_role" "test_wildcard" {
  name               = "iaminv-test-wildcard"
  assume_role_policy = data.aws_iam_policy_document.trust_lambda.json
}

resource "aws_iam_role_policy" "test_wildcard" {
  name = "everything"
  role = aws_iam_role.test_wildcard.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "*", Resource = "*" }]
  })
}

# Expect: PASSROLE_BROAD_RESOURCE
resource "aws_iam_role" "test_passrole" {
  name               = "iaminv-test-passrole"
  assume_role_policy = data.aws_iam_policy_document.trust_lambda.json
}

resource "aws_iam_role_policy" "test_passrole" {
  name = "passrole"
  role = aws_iam_role.test_passrole.id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["iam:PassRole", "ecs:*"], Resource = "*" }]
  })
}

# Expect: TRUST_EXTERNAL_ACCOUNT (high, no condition)
resource "aws_iam_role" "test_external_trust" {
  name = "iaminv-test-external-trust"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { AWS = "arn:aws:iam::${var.external_account_id}:root" }
    }]
  })
}

# Expect: TRUST_ANY_PRINCIPAL at medium severity (the org condition lowers it from critical).
resource "aws_iam_role" "test_conditional_wildcard_trust" {
  name = "iaminv-test-conditional-wildcard"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Action    = "sts:AssumeRole"
      Principal = { AWS = "*" }
      Condition = { StringEquals = { "aws:PrincipalOrgID" = "o-exampleorgid" } }
    }]
  })
}

# Unused role with a permissions boundary and an inline policy, for attachment-table coverage.
resource "aws_iam_policy" "boundary" {
  name = "iaminv-test-boundary"
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = ["s3:Get*", "s3:List*"], Resource = "*" }]
  })
}

resource "aws_iam_role" "test_unused_bounded" {
  name                 = "iaminv-test-unused-bounded"
  assume_role_policy   = data.aws_iam_policy_document.trust_lambda.json
  permissions_boundary = aws_iam_policy.boundary.arn
}

# A user to create an access key for by hand (`aws iam create-access-key`). OLD_ACCESS_KEY only fires
# after 90 days; to test sooner set MAX_KEY_AGE_DAYS=1 in the Lambda environment.
resource "aws_iam_user" "test_user" {
  name = "iaminv-test-user"
}

output "account_id" {
  value = data.aws_caller_identity.current.account_id
}
