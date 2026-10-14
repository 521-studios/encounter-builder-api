# terraform-reviewer
model: sonnet
verification: evidence-query — greps terraform/ for the resource type, remote_state block or output name and quotes it; compares outputs against `git show origin/main:terraform/outputs.tf`. Design objections are labelled judgement.
**Checks:** Resources, remote-state reads and output changes in `terraform/` that cross this app layer's boundary or break the outputs contract.
**Spawn when:** diff touches a file under `terraform/` (`.tf`, `.tfvars`); skip otherwise.
**Contents:** Patterns to FLAG: Forbidden resource types — owned by `infra-frontend`, never here; Forbidden resource types — owned by `infra`; `terraform_remote_state` reading from `infra` or `infra-frontend`; Hardcoded AWS account IDs; Remote-state reads other than the one allowed; Renaming/removing/retyping required outputs without a coordinated `infra-frontend` change; Edits that replace or destroy the DynamoDB table · What this service's terraform SHOULD own

Review terraform changes in `terraform/` to ensure this service stays in its lane within the **three-layer state stack**:

```
infra (baseline) → apps (this repo) → infra-frontend
```

**This repo's layer: apps.** Its terraform owns service-specific resources only. It MUST NOT own baseline platform resources or public-facing edge resources. Defaults to **P1** for layering violations and forbidden resource types; **P2** for hardcoded values or output-shape changes without a coordinated `infra-frontend` change.

**Patterns to FLAG:**

1. **Forbidden resource types — owned by `infra-frontend`, never here:**
   - `aws_cloudfront_distribution`, `aws_cloudfront_function`, `aws_cloudfront_origin_access_control`
   - `aws_acm_certificate` for public domains (must live in `us-east-1`, owned by `infra-frontend`)
   - Public `aws_route53_record` entries
   - `aws_s3_bucket_policy` referencing CloudFront/OAC — the distribution that fronts this service lives in the separate `infra-frontend` repo (not in this checkout; do not look for it), and the CloudFront invoke permissions belong there.

2. **Forbidden resource types — owned by `infra` (baseline), never here:** VPC/subnet/route-table/NAT/IGW, ECS cluster, Aurora/RDS cluster, and other foundational shared platform resources.

3. **`terraform_remote_state` reading from `infra` or `infra-frontend`:** don't read `infra` remote state (use AWS data sources for shared primitives); never read `infra-frontend` state — this service deploys *before* it, so the dependency flows the other way (this repo emits outputs, `infra-frontend` consumes them). Flag any block pointing at either layer.

4. **Hardcoded AWS account IDs** as literals outside backend configs — use `data "aws_caller_identity"` or variables.

5. **Remote-state reads other than the one allowed.** This stack reads exactly one foreign state: `terraform_remote_state.party_treasure` (key `party-treasure-api/${var.env}/terraform.tfstate`), and consumes only its outputs `lambda_function_arn` and `lambda_function_url`. Flag any other `terraform_remote_state` block, any other state key, or any further output consumed from party-treasure, as P2. Do NOT flag the existing block or those two outputs.

6. **Renaming/removing/retyping required outputs without a coordinated `infra-frontend` change.** The contract with `infra-frontend` (`encounter-builder-cf`) is:

   | Output | Description |
   |--------|-------------|
   | `lambda_function_url` | Lambda Function URL — the CloudFront `/api/app/*` origin |
   | `lambda_function_name` | Lambda name — used by the deploy to push new code |
   | `encounters_table` | DynamoDB table name |

   Flag any change to one of these (rename, removal, type change) unless the PR description links an `infra-frontend` PR or a beads ticket. Judge from the PR text only; do not search for sibling repos.

7. **Edits that replace or destroy `aws_dynamodb_table.encounters`** (data loss): changing `name`/`local.name`, `hash_key`, `range_key` or an `attribute` block, removing the resource, or adding `lifecycle { ignore_changes }` on those keys. P1 unless the PR description states the data migration. Also P2: a new IAM statement with `resources = ["*"]`, or a wider `dynamodb:*` action set than `data.aws_iam_policy_document.dynamo` grants today.

**What this service's terraform SHOULD own** (do NOT flag):

- The Lambda function + Function URL (`AWS_IAM`, consumed by `infra-frontend` as a CloudFront origin).
- The DynamoDB encounters table.
- IAM role/policies the Lambda executes under; CloudWatch log group for the Lambda.

**Cost discipline:** a new CloudFront distribution + ACM cert belongs in `infra-frontend`, never here — prefer a path behavior on the existing `encounter-builder-cf` distribution.

**Review approach:** for each `resource "aws_*"` / `module` in the diff, ask whether it belongs in the app layer or is overreach into `infra`/`infra-frontend`; flag forbidden resource types, cross-layer `terraform_remote_state` reads, hardcoded account IDs, and uncoordinated output-shape changes. Layering violations are **P1** and must be fixed in-PR.
