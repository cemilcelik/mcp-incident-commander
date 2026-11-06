# AWS Application Load Balancer (Sticky Session KAPALI) ve SEP-2243 L7 Header Routing kuralları.
resource "aws_security_group" "alb_sg" {
  name        = "${var.project_name}-${var.environment}-alb-sg"
  description = "Security group for Stateless MCP Application Load Balancer"
  vpc_id      = var.vpc_id

  ingress {
    description = "Allow HTTP inbound traffic"
    from_port   = 80
    to_port     = 80
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    description = "Allow all outbound traffic"
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_lb" "mcp_alb" {
  name               = "${var.project_name}-${var.environment}-alb"
  internal           = false
  load_balancer_type = "application"
  security_groups    = [aws_security_group.alb_sg.id]
  subnets            = var.public_subnet_ids

  drop_invalid_header_fields = true
}

resource "aws_lb_target_group" "mcp_lambda_tg" {
  name        = "${var.project_name}-${var.environment}-tg"
  target_type = "lambda"

  # Stateless MCP 2026-07-28 kanıtı: Sticky Session KESİNLİKLE KAPALIDIR.
  stickiness {
    enabled = false
    type    = "lb_cookie"
  }

  health_check {
    enabled             = true
    path                = "/healthz"
    matcher             = "200"
    interval            = 30
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 2
  }
}

resource "aws_lb_target_group_attachment" "mcp_lambda_attach" {
  target_group_arn = aws_lb_target_group.mcp_lambda_tg.arn
  target_id        = aws_lambda_function.mcp_server.arn
  depends_on       = [aws_lambda_permission.allow_alb]
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.mcp_alb.arn
  port              = 80
  protocol          = "HTTP"

  # Varsayılan eylem: SEP-2243 Mcp-Method başlığı taşımayan istekleri L7 sınırında (Lambda çağrılmadan) 400 ile reddet.
  default_action {
    type = "fixed-response"

    fixed_response {
      content_type = "application/json"
      message_body = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32600,\"message\":\"rejected at ALB L7: missing or invalid SEP-2243 Mcp-Method header\"}}"
      status_code  = "400"
    }
  }
}

# Kural 1 (Öncelik 10): Yasaklı Mcp-Session-Id başlığı içeren tüm istekleri L7 sınırında reddet.
resource "aws_lb_listener_rule" "reject_mcp_session_id" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 10

  action {
    type = "fixed-response"

    fixed_response {
      content_type = "application/json"
      message_body = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32002,\"message\":\"rejected at ALB L7: Mcp-Session-Id is forbidden in Stateless MCP 2026-07-28\"}}"
      status_code  = "400"
    }
  }

  condition {
    http_header {
      http_header_name = "Mcp-Session-Id"
      values           = ["*"]
    }
  }
}

# Kural 2 (Öncelik 20): Mcp-Method = server/discover veya tools/list olan /mcp isteklerini Lambda hedef grubuna yönlendir.
resource "aws_lb_listener_rule" "forward_mcp_discovery_and_list" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 20

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.mcp_lambda_tg.arn
  }

  condition {
    path_pattern {
      values = ["/mcp"]
    }
  }

  condition {
    http_header {
      http_header_name = "Mcp-Method"
      values           = ["server/discover", "tools/list"]
    }
  }
}

# Kural 3 (Öncelik 30): Mcp-Method = tools/call VE geçerli Mcp-Name başlığı taşıyan /mcp isteklerini Lambda hedef grubuna yönlendir.
resource "aws_lb_listener_rule" "forward_mcp_tools_call" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 30

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.mcp_lambda_tg.arn
  }

  condition {
    path_pattern {
      values = ["/mcp"]
    }
  }

  condition {
    http_header {
      http_header_name = "Mcp-Method"
      values           = ["tools/call"]
    }
  }

  condition {
    http_header {
      http_header_name = "Mcp-Name"
      values = [
        "incident_context_manager",
        "execute_mitigation_plan",
        "live_telemetry_diagnostic",
      ]
    }
  }
}

# Kural 4 (Öncelik 40): /healthz sağlık kontrolü isteklerini Lambda hedef grubuna yönlendir.
resource "aws_lb_listener_rule" "forward_healthz" {
  listener_arn = aws_lb_listener.http.arn
  priority     = 40

  action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.mcp_lambda_tg.arn
  }

  condition {
    path_pattern {
      values = ["/healthz"]
    }
  }
}
