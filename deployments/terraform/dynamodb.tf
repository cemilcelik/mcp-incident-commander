# Stateless MCP sunucu kopyalarının uygulama durumunu (IncidentContext & SEP-2322 Continuation) paylaştığı DynamoDB tablosu.
resource "aws_dynamodb_table" "incident_state" {
  name         = "${var.project_name}-${var.environment}-state"
  billing_mode = "PAY_PER_REQUEST" # On-Demand ölçekleme (önceden kapasite tahsisi gerektirmez)
  hash_key     = "pk"              # TENANT#<tenant_id>
  range_key    = "sk"              # INCIDENT#<id> veya CONTINUATION#<token>

  attribute {
    name = "pk"
    type = "S"
  }

  attribute {
    name = "sk"
    type = "S"
  }

  # Süresi dolan olay ve SEP-2322 continuation jetonlarının otomatik temizlenmesi için TTL aktif.
  ttl {
    attribute_name = "ttl"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  server_side_encryption {
    enabled = true
  }
}
