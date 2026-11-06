package mcpclient

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/server"
	"mcp-incident-commander/internal/store"
)

// roundRobinALBTransport sticky session kapalı bir AWS ALB'nin istekleri farklı Lambda kopyalarına dağıtmasını simüle eder.
type roundRobinALBTransport struct {
	backends []http.Handler
	counter  atomic.Uint64
	listHits atomic.Uint64
}

func (t *roundRobinALBTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get(mcp.HeaderMCPMethod) == mcp.MethodToolsList {
		t.listHits.Add(1)
	}
	idx := t.counter.Add(1) % uint64(len(t.backends))
	rec := httptest.NewRecorder()
	t.backends[idx].ServeHTTP(rec, req)
	return rec.Result(), nil
}

func TestClient_StatelessLifecycleAndMultiRoundTrip(t *testing.T) {
	t.Parallel()

	secret := []byte("client-test-hmac-secret-2026")
	sharedStore := store.NewInMemoryStore()
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	r1, _ := server.NewRouter(server.Config{
		InstanceID:         "lambda-az1",
		JWTSigningSecret:   secret,
		Store:              sharedStore,
		TelemetryStepDelay: 1 * time.Millisecond,
		Logger:             discardLogger,
	})
	r2, _ := server.NewRouter(server.Config{
		InstanceID:         "lambda-az2",
		JWTSigningSecret:   secret,
		Store:              sharedStore,
		TelemetryStepDelay: 1 * time.Millisecond,
		Logger:             discardLogger,
	})

	alb := &roundRobinALBTransport{backends: []http.Handler{r1, r2}}
	token, err := middleware.GenerateToken(secret, "tenant-acme", "eng-sre-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	var (
		progressStages []int
		approvalCalled bool
	)

	client, err := New(Config{
		Endpoint:    "http://alb.internal/mcp",
		BearerToken: token,
		HTTPClient:  &http.Client{Transport: alb},
		ProgressHandler: func(notif mcp.ProgressNotificationParams) {
			progressStages = append(progressStages, notif.Progress)
		},
		ApprovalHandler: func(_ context.Context, inputReq mcp.InputRequiredResult) (bool, string, error) {
			approvalCalled = true
			if inputReq.ContinuationToken == "" {
				t.Error("expected non-empty continuation_token in ApprovalHandler")
			}
			return true, "Approved by automated client test", nil
		},
	})
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	ctx := context.Background()

	// 1. server/discover: initialize olmadan doğrudan keşif.
	disc, err := client.Discover(ctx)
	if err != nil || !disc.ServerInfo.Stateless || disc.ProtocolVersion != mcp.ProtocolVersion {
		t.Fatalf("Discover failed: res=%+v, err=%v", disc, err)
	}

	// 2. tools/list ve SEP-2549 istemci önbelleklemesi (2 çağrıda sunucuya sadece 1 istek gitmeli).
	tools1, err := client.ListTools(ctx)
	if err != nil || len(tools1.Tools) != 3 {
		t.Fatalf("ListTools failed: res=%+v, err=%v", tools1, err)
	}
	_, _ = client.ListTools(ctx)
	if hits := alb.listHits.Load(); hits != 1 {
		t.Fatalf("expected SEP-2549 client cache to serve 2nd ListTools call (hits=1), got hits=%d", hits)
	}

	// 3. SEP-2322 Multi-Round-Trip: 1. tur Lambda #1'de InputRequired döner, ApprovalHandler 2. turu Lambda #2'de tamamlar.
	mitigateRes, err := client.CallTool(ctx, mcp.ToolExecuteMitigationPlan, map[string]any{
		"incident_id":        "INC-500",
		"action":             "drain_az_traffic",
		"target_resource":    "alb-prod-eu",
		"risk_level":         "high",
		"estimated_cost_usd": 900.0,
	})
	if err != nil || !approvalCalled || mitigateRes.Status != mcp.StatusCompleted {
		t.Fatalf("expected SEP-2322 auto-approved completion, got: res=%+v, approvalCalled=%v, err=%v", mitigateRes, approvalCalled, err)
	}

	// 4. Streamable HTTP (SSE): live_telemetry_diagnostic %25, %50, %75, %100 ilerlemelerini anlık ayrıştırır.
	sseRes, err := client.CallTool(ctx, mcp.ToolLiveTelemetryDiagnostic, map[string]any{
		"incident_id":    "INC-500",
		"target_service": "orders-api",
	})
	if err != nil || sseRes.Status != mcp.StatusCompleted || len(progressStages) != 4 {
		t.Fatalf("expected 4 SSE progress stages and completed status, got stages=%v, res=%+v, err=%v", progressStages, sseRes, err)
	}
	if !strings.Contains(sseRes.Content[0].Text, "orders-api") {
		t.Fatalf("unexpected SSE final content: %s", sseRes.Content[0].Text)
	}
}
