# AWS Lambda ARM64 (provided.al2023) fonksiyonu ve CloudWatch Log grubu tanımı.
resource "aws_cloudwatch_log_group" "mcp_lambda" {
  name              = "/aws/lambda/${var.project_name}-${var.environment}"
  retention_in_days = 14
}

resource "aws_lambda_function" "mcp_server" {
  function_name = "${var.project_name}-${var.environment}"
  description   = "Stateless MCP 2026-07-28 Incident Commander Server (ARM64)"
  role          = aws_iam_role.lambda_exec.arn

  filename         = var.lambda_package_path
  source_code_hash = fileexists(var.lambda_package_path) ? filebase64sha256(var.lambda_package_path) : null

  runtime       = "provided.al2023"
  architectures = ["arm64"]
  handler       = "bootstrap"

  memory_size = var.lambda_memory_size
  timeout     = var.lambda_timeout_seconds

  environment {
    variables = {
      DYNAMODB_TABLE_NAME = aws_dynamodb_table.incident_state.name
      JWT_SIGNING_SECRET  = var.jwt_signing_secret
    }
  }

  depends_on = [
    aws_cloudwatch_log_group.mcp_lambda,
    aws_iam_role_policy.lambda_least_privilege,
  ]
}

# Application Load Balancer'ın Lambda fonksiyonunu tetikleyebilmesi için kaynak tabanlı izin.
resource "aws_lambda_permission" "allow_alb" {
  statement_id  = "AllowExecutionFromALB"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.mcp_server.function_name
  principal     = "elasticloadbalancing.amazonaws.com"
  source_arn    = aws_lb_target_group.mcp_lambda_tg.arn
}
