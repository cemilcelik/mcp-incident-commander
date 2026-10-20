package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-incident-commander/internal/mcp"
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

func TestMitigationPlanTool_SEP2322MultiRoundTrip(t *testing.T) {
	t.Parallel()

	sharedStore := store.NewInMemoryStore()
	lambdaInstance1 := NewMitigationPlanTool(sharedStore, "lambda-instance-az1")
	lambdaInstance2 := NewMitigationPlanTool(sharedStore, "lambda-instance-az2")

	ctx := middleware.WithIdentity(context.Background(), middleware.Identity{
		TenantID:   "tenant-finops",
		EngineerID: "eng-commander-1",
	})

	// 1. Düşük maliyetli (<= $500) ve düşük riskli işlem tek turda tamamlanır.
	lowCostRes, rpcErr := lambdaInstance1.Execute(ctx, json.RawMessage(`{
		"incident_id": "INC-2026-901",
		"action": "enable_s3_gateway_endpoint",
		"target_resource": "vpc-0a1b2c3d",
		"risk_level": "low",
		"estimated_cost_usd": 0
	}`), "")
	if rpcErr != nil || lowCostRes.Status != mcp.StatusCompleted {
		t.Fatalf("expected immediate completion for low-cost plan, got: %+v, err: %v", lowCostRes, rpcErr)
	}

	// 2. Tur 1 (Lambda #1): Yüksek maliyetli (> $500) ve yüksek riskli işlem InputRequiredResult döner.
	round1Res, rpcErr := lambdaInstance1.Execute(ctx, json.RawMessage(`{
		"incident_id": "INC-2026-901",
		"action": "scale_out_eks_nodegroup",
		"target_resource": "eks-prod-core-ng",
		"risk_level": "high",
		"estimated_cost_usd": 1450.00
	}`), "")
	if rpcErr != nil || round1Res.Status != mcp.StatusInputRequired || round1Res.InputRequired == nil {
		t.Fatalf("expected SEP-2322 input_required result, got: %+v, err: %v", round1Res, rpcErr)
	}

	contToken := round1Res.InputRequired.ContinuationToken
	if contToken == "" {
		t.Fatal("expected non-empty continuation_token")
	}

	// 3. Tur 2 (Lambda #2): Farklı Lambda kopyası continuation_token ile onayı tamamlar.
	round2Res, rpcErr := lambdaInstance2.Execute(ctx, json.RawMessage(`{
		"approved": true,
		"approver_comment": "Approved by FinOps Commander for peak traffic"
	}`), contToken)
	if rpcErr != nil || round2Res.Status != mcp.StatusCompleted {
		t.Fatalf("expected completed mitigation on round 2, got: %+v, err: %v", round2Res, rpcErr)
	}

	var execResp MitigationExecutionResponse
	if err := json.Unmarshal([]byte(round2Res.Content[0].Text), &execResp); err != nil {
		t.Fatalf("failed to unmarshal execution response: %v", err)
	}
	if !execResp.ApprovedRoundTrip || execResp.HandledByInstance != "lambda-instance-az2" || execResp.Status != MitigationStatusExecuted {
		t.Fatalf("unexpected round 2 execution response: %+v", execResp)
	}

	// Edge case: Aynı continuation_token tekrar kullanılırsa ErrCodeContinuationExpired (-32003) dönmeli.
	_, replayErr := lambdaInstance1.Execute(ctx, json.RawMessage(`{"approved": true}`), contToken)
	if replayErr == nil || replayErr.Code != mcp.ErrCodeContinuationExpired {
		t.Fatalf("expected ErrCodeContinuationExpired on replay, got: %v", replayErr)
	}
}

func TestLiveTelemetryTool_StreamableHTTPSSE(t *testing.T) {
	t.Parallel()

	sharedStore := store.NewInMemoryStore()
	telemetryTool := NewLiveTelemetryTool(sharedStore, "lambda-instance-sse-1", 1*time.Millisecond)

	// Edge case: Eksik target_service parametresi SSE akışı başlamadan önce -32602 dönmeli.
	authCtx := middleware.WithIdentity(context.Background(), middleware.Identity{
		TenantID:   "tenant-finops",
		EngineerID: "eng-sre-9",
	})
	rec := httptest.NewRecorder()
	preErr := telemetryTool.StreamExecute(authCtx, rec, "req-bad", "prog-1", json.RawMessage(`{"incident_id": "INC-905"}`))
	if preErr == nil || preErr.Code != mcp.ErrCodeInvalidParams {
		t.Fatalf("expected ErrCodeInvalidParams for missing target_service, got: %v", preErr)
	}

	// Happy path: httptest.NewServer üzerinden gerçek zamanlı Chunked SSE (%25, %50, %75, %100 + final message) okuma.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := middleware.WithIdentity(r.Context(), middleware.Identity{
			TenantID:   "tenant-finops",
			EngineerID: "eng-sre-9",
		})
		rpcErr := telemetryTool.StreamExecute(ctx, w, "req-sse-42", "prog-42", json.RawMessage(`{
			"incident_id": "INC-2026-905",
			"target_service": "checkout-payment-api",
			"window_minutes": 30
		}`))
		if rpcErr != nil {
			t.Errorf("unexpected StreamExecute error: %v", rpcErr)
		}
	}))
	defer srv.Close()

	resp, err := srv.Client().Post(srv.URL, "application/json", nil)
	if err != nil {
		t.Fatalf("failed to connect to SSE test server: %v", err)
	}
	defer resp.Body.Close()

	if gotCT := resp.Header.Get("Content-Type"); gotCT != ContentTypeEventStream {
		t.Fatalf("expected Content-Type %q, got %q", ContentTypeEventStream, gotCT)
	}

	var (
		progressValues []int
		finalMessage   string
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if strings.Contains(payload, `"method":"notifications/progress"`) {
			var notif struct {
				Params mcp.ProgressNotificationParams `json:"params"`
			}
			if err := json.Unmarshal([]byte(payload), &notif); err != nil {
				t.Fatalf("failed to unmarshal progress notification: %v", err)
			}
			progressValues = append(progressValues, notif.Params.Progress)
		} else if strings.Contains(payload, `"id":"req-sse-42"`) {
			finalMessage = payload
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scanner error while reading SSE stream: %v", err)
	}

	expectedProgress := []int{25, 50, 75, 100}
	if len(progressValues) != len(expectedProgress) {
		t.Fatalf("expected %v progress stages, got %v", expectedProgress, progressValues)
	}
	for i, want := range expectedProgress {
		if progressValues[i] != want {
			t.Fatalf("stage %d: expected %d%%, got %d%%", i, want, progressValues[i])
		}
	}
	if !strings.Contains(finalMessage, `"status":"completed"`) || !strings.Contains(finalMessage, "checkout-payment-api") {
		t.Fatalf("unexpected final SSE JSON-RPC response: %s", finalMessage)
	}
}


