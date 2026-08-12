variable "env" {
  description = "Deployment environment (staging | production)"
  type        = string
}

variable "aws_region" {
  description = "AWS region"
  type        = string
  default     = "us-east-2"
}

variable "lambda_zip_path" {
  description = "Path to the built Lambda bootstrap zip"
  type        = string
  default     = "../function.zip"
}

variable "oidc_issuer" {
  description = "lets-roll OIDC issuer this env verifies tokens against"
  type        = string
  # e.g. https://lets-roll.staging.521studios.com — set per env by the deploy.
}

variable "oidc_audience" {
  description = "Required JWT audience (SPA client_id); empty accepts any"
  type        = string
  default     = "encounter-builder-web"
}
