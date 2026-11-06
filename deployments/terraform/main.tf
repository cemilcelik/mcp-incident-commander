# Terraform ve AWS Provider temel yapılandırması.
terraform {
  required_version = ">= 1.6.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Project     = var.project_name
      Environment = var.environment
      Protocol    = "Stateless-MCP-2026-07-28"
      ManagedBy   = "Terraform"
    }
  }
}
