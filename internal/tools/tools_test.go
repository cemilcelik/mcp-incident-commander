package tools

import (
	"context"
	"encoding/json"
	"testing"

	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/store"
)

func TestIncidentContextTool_StatelessMultiInstanceStateSharing(t *testing.T) {
	t.Parallel()

	sharedStore := store.NewInMemoryStore()

	// Sticky session kapalı ALB arkasındaki iki ayrı Lambda kopyasını simüle eder.
	lambdaInstance1 := NewIncidentContextTool(sharedStore, "lambda-instance-az1")
	lambdaInstance2 := NewIncidentContextTool(sharedStore, "lambda-instance-az2")

	ctxEng1 := middleware.WithIdentity(context.Background(), middleware.Identity{
		TenantID:   "tenant-finops",
		EngineerID: "eng-oncall-1",
	})
	ctxEng2 := middleware.WithIdentity(context.Background(), middleware.Identity{
		TenantID:   "tenant-finops",
		EngineerID: "eng-oncall-2",
	})

	// 1. İstek Lambda #1'e düşer: Olay oluşturulur.
	createRes, rpcErr := lambdaInstance1.Execute(ctxEng1, json.RawMessage(`{
		"action": "create",
		"incident_id": "INC-2026-900",
		"title": "NAT Gateway Data Processing Cost Anomaly",
		"severity": "high",
		"note": "Initial alert triggered by FinOps anomaly detector"
	}`))
	if rpcErr != nil || createRes.IsError {
		t.Fatalf("create on instance 1 failed: rpcErr=%v, res=%+v", rpcErr, createRes)
	}

	// 2. İstek Lambda #2'ye düşer: Bellekte oturum olmamasına rağmen aynı olaya not eklenir.
	appendRes, rpcErr := lambdaInstance2.Execute(ctxEng2, json.RawMessage(`{
		"action": "append_note",
		"incident_id": "INC-2026-900",
		"note": "Identified cross-AZ S3 traffic missing VPC Gateway Endpoint"
	}`))
	if rpcErr != nil || appendRes.IsError {
		t.Fatalf("append_note on instance 2 failed: rpcErr=%v, res=%+v", rpcErr, appendRes)
	}

	// 3. İstek tekrar Lambda #1'e düşer: Her iki mühendisin notları eksiksiz okunur.
	getRes, rpcErr := lambdaInstance1.Execute(ctxEng1, json.RawMessage(`{
		"action": "get",
		"incident_id": "INC-2026-900"
	}`))
	if rpcErr != nil || getRes.IsError {
		t.Fatalf("get on instance 1 failed: rpcErr=%v, res=%+v", rpcErr, getRes)
	}

	var parsed IncidentContextResponse
	if err := json.Unmarshal([]byte(getRes.Content[0].Text), &parsed); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if parsed.HandledByInstance != "lambda-instance-az1" || len(parsed.Incident.Notes) != 2 {
		t.Fatalf("expected 2 notes across stateless instances, got: %+v", parsed)
	}

	// Edge case: Farklı kiracı (tenant-other) aynı incident_id'yi okuyamaz.
	ctxOtherTenant := middleware.WithIdentity(context.Background(), middleware.Identity{
		TenantID:   "tenant-other",
		EngineerID: "eng-ext",
	})
	isolatedRes, rpcErr := lambdaInstance2.Execute(ctxOtherTenant, json.RawMessage(`{
		"action": "get",
		"incident_id": "INC-2026-900"
	}`))
	if rpcErr != nil || !isolatedRes.IsError {
		t.Fatalf("expected tenant isolation error result, got: %+v, rpcErr=%v", isolatedRes, rpcErr)
	}
}
