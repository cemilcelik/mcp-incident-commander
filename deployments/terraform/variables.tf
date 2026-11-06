# Altyapı yapılandırma değişkenleri (gizli değerler varsayılan olarak gömülmez).
variable "aws_region" {
  description = "AWS deployment region"
  type        = string
  default     = "eu-central-1"
}

variable "project_name" {
  description = "Project identifier prefix for AWS resources"
  type        = string
  default     = "mcp-incident-commander"
}

variable "environment" {
  description = "Deployment environment name (e.g. dev, staging, prod)"
  type        = string
  default     = "prod"
}

variable "vpc_id" {
  description = "Target VPC ID for the Application Load Balancer"
  type        = string
}

variable "public_subnet_ids" {
  description = "List of public subnet IDs across at least two Availability Zones for ALB"
  type        = list(string)
}

variable "lambda_package_path" {
  description = "Path to the compiled ARM64 Go Lambda deployment zip archive (containing 'bootstrap')"
  type        = string
  default     = "../../bin/mcp-server-lambda-arm64.zip"
}

variable "jwt_signing_secret" {
  description = "HMAC signing secret for Stateless JWT verification (injected via TF_VAR_jwt_signing_secret or secret manager)"
  type        = string
  sensitive   = true
}

variable "lambda_memory_size" {
  description = "Memory allocation in MB for the ARM64 Lambda function"
  type        = number
  default     = 256
}

variable "lambda_timeout_seconds" {
  description = "Maximum execution timeout in seconds for Lambda invocations"
  type        = number
  default     = 30
}
