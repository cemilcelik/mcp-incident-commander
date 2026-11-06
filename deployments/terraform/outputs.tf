# Dağıtım sonrası üretilen altyapı uç noktası ve kaynak kimliği çıktıları.
output "alb_dns_name" {
  description = "Public DNS name of the Stateless MCP Application Load Balancer"
  value       = aws_lb.mcp_alb.dns_name
}

output "mcp_endpoint_url" {
  description = "Full HTTP URL for Stateless MCP 2026-07-28 requests"
  value       = "http://${aws_lb.mcp_alb.dns_name}/mcp"
}

output "dynamodb_table_name" {
  description = "Name of the DynamoDB application state table"
  value       = aws_dynamodb_table.incident_state.name
}

output "dynamodb_table_arn" {
  description = "ARN of the DynamoDB application state table"
  value       = aws_dynamodb_table.incident_state.arn
}

output "lambda_function_arn" {
  description = "ARN of the ARM64 Stateless MCP Lambda function"
  value       = aws_lambda_function.mcp_server.arn
}
