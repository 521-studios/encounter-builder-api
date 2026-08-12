terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
  backend "s3" {
    # bucket + key are passed via -backend-config at init (see deploy.yml)
    region = "us-east-2"
  }
}

provider "aws" {
  region = var.aws_region
}

locals {
  name = "encounter-builder-api-${var.env}"
  tags = {
    Project     = "encounter-builder-api"
    Environment = var.env
    ManagedBy   = "terraform"
  }
}

# ─── DynamoDB: encounters single table ───────────────────────────────────────
# Single-table design keyed PK (CAMPAIGN#<game_id>) / SK (ENCOUNTER#<ulid>, …);
# on-demand billing (≈$0 at this traffic). This is the durable store — a
# destroy here is data loss, which is why the deploy's destroy guard matters.
resource "aws_dynamodb_table" "encounters" {
  name         = local.name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "pk"
  range_key    = "sk"

  attribute {
    name = "pk"
    type = "S"
  }
  attribute {
    name = "sk"
    type = "S"
  }

  point_in_time_recovery {
    enabled = var.env == "production"
  }

  tags = local.tags
}

# ─── IAM ─────────────────────────────────────────────────────────────────────
data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda" {
  name               = local.name
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
  tags               = local.tags
}

resource "aws_iam_role_policy_attachment" "basic" {
  role       = aws_iam_role.lambda.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
}

data "aws_iam_policy_document" "dynamo" {
  statement {
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
      "dynamodb:DeleteItem",
      "dynamodb:Query",
    ]
    resources = [
      aws_dynamodb_table.encounters.arn,
      "${aws_dynamodb_table.encounters.arn}/index/*",
    ]
  }
}

resource "aws_iam_role_policy" "dynamo" {
  name   = "${local.name}-dynamo"
  role   = aws_iam_role.lambda.id
  policy = data.aws_iam_policy_document.dynamo.json
}

# ─── Lambda ──────────────────────────────────────────────────────────────────
resource "aws_cloudwatch_log_group" "api" {
  name              = "/aws/lambda/${local.name}"
  retention_in_days = 14
  tags              = local.tags
}

resource "aws_lambda_function" "api" {
  function_name    = local.name
  role             = aws_iam_role.lambda.arn
  filename         = var.lambda_zip_path
  source_code_hash = filebase64sha256(var.lambda_zip_path)
  handler          = "bootstrap"
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  memory_size      = 512
  timeout          = 30

  environment {
    variables = {
      ENV              = var.env
      ENCOUNTERS_TABLE = aws_dynamodb_table.encounters.name
      OIDC_ISSUER      = var.oidc_issuer
      OIDC_AUDIENCE    = var.oidc_audience
    }
  }

  depends_on = [aws_cloudwatch_log_group.api]
  tags       = local.tags
}

# Function URL is IAM-auth'd and reached only through CloudFront (OAC + SigV4);
# app-level authorization is the JWT the Go code verifies.
resource "aws_lambda_function_url" "api" {
  function_name      = aws_lambda_function.api.function_name
  authorization_type = "AWS_IAM"
}

# CloudFront needs BOTH permissions to invoke a Function-URL origin.
resource "aws_lambda_permission" "cloudfront_invoke_url" {
  statement_id  = "AllowCloudFrontInvokeFunctionUrl"
  action        = "lambda:InvokeFunctionUrl"
  function_name = aws_lambda_function.api.function_name
  principal     = "cloudfront.amazonaws.com"
}

resource "aws_lambda_permission" "cloudfront_invoke" {
  statement_id  = "AllowCloudFrontInvokeFunction"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.api.function_name
  principal     = "cloudfront.amazonaws.com"
}
