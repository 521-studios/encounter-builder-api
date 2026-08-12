output "lambda_function_url" {
  description = "Function URL — consumed by infra-frontend as the /api/app/* CloudFront origin"
  value       = aws_lambda_function_url.api.function_url
}

output "lambda_function_name" {
  description = "Lambda name — used by the deploy to push new code"
  value       = aws_lambda_function.api.function_name
}

output "encounters_table" {
  description = "DynamoDB table name"
  value       = aws_dynamodb_table.encounters.name
}
