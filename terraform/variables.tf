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

variable "tf_state_bucket" {
  description = "S3 bucket holding this env's terraform state — also where party-treasure-api's remote state is read from (§5b). Matches the backend bucket passed at init."
  type        = string
}

variable "oidc_issuer" {
  description = "lets-roll OIDC issuer this env verifies tokens against"
  type        = string
  # e.g. https://lets-roll.staging.521studios.com — set per env by the deploy.
}

variable "lets_roll_url" {
  description = "lets-roll API base URL (its /api/v1 and the combat push). Today it equals oidc_issuer; it stays lets-roll when the issuer moves to the auth service (we5k.2.6)."
  type        = string
  # e.g. https://lets-roll.staging.521studios.com — set per env by the deploy.
}

variable "svc_oidc_issuer" {
  description = "Issuer for service tokens (X-Service-Token, we5k.2.6). Empty makes the Go code default to oidc_issuer, the pre-cut-over behaviour."
  type        = string
  default     = ""
}

variable "oidc_audience" {
  description = "Required JWT audience (SPA client_id); empty accepts any"
  type        = string
  default     = "encounter-builder-web"
}
