# En az yetki prensibine (Least Privilege) uygun AWS Lambda çalışma rolü ve IAM politikası.
data "aws_iam_policy_document" "lambda_assume_role" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]

    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "lambda_exec" {
  name               = "${var.project_name}-${var.environment}-lambda-role"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume_role.json
}

data "aws_iam_policy_document" "lambda_least_privilege" {
  # Yalnızca ilgili DynamoDB tablosu üzerinde nokta okuma/yazma/güncelleme yetkisi (Scan ve DeleteTable kapalı).
  statement {
    sid    = "DynamoDBIncidentStateLeastPrivilege"
    effect = "Allow"
    actions = [
      "dynamodb:GetItem",
      "dynamodb:PutItem",
      "dynamodb:UpdateItem",
    ]
    resources = [
      aws_dynamodb_table.incident_state.arn,
    ]
  }

  # Yalnızca bu fonksiyona ait CloudWatch Log grubuna log yazma yetkisi.
  statement {
    sid    = "CloudWatchLogsLeastPrivilege"
    effect = "Allow"
    actions = [
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]
    resources = [
      "${aws_cloudwatch_log_group.mcp_lambda.arn}:*",
    ]
  }
}

resource "aws_iam_role_policy" "lambda_least_privilege" {
  name   = "${var.project_name}-${var.environment}-least-privilege"
  role   = aws_iam_role.lambda_exec.id
  policy = data.aws_iam_policy_document.lambda_least_privilege.json
}
