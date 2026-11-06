package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/store"
)

func TestRouter_EndToEndStatelessDispatch(t *testing.T) {
	t.Parallel()

	secret := []byte("router-test-secret-key-2026")
	sharedStore := store.NewInMemoryStore()
	discardLogger := slog.New(slog.NewTextHandler(io.Discard, nil))

	router1, err := NewRouter(Config{
		InstanceID:         "lambda-node-1",
		JWTSigningSecret:   secret,
		Store:              sharedStore,
		TelemetryStepDelay: 1 * time.Millisecond,
		Logger:             discardLogger,
	})
	if err != nil {
		t.Fatalf("failed to create router1: %v", err)
	}

	router2, err := NewRouter(Config{
		InstanceID:         "lambda-node-2",
		JWTSigningSecret:   secret,
		Store:              sharedStore,
		TelemetryStepDelay: 1 * time.Millisecond,
		Logger:             discardLogger,
	})
	if err != nil {
		t.Fatalf("failed to create router2: %v", err)
	}

	token, err := middleware.GenerateToken(secret, "tenant-prod", "eng-lead-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	// 1. Health check (/healthz) JWT veya MCP başlığı gerektirmeden 200 OK dönmeli.
	healthReq := httptest.NewRequest(http.MethodGet, EndpointHealth, nil)
	healthRec := httptest.NewRecorder()
	router1.ServeHTTP(healthRec, healthReq)
	if healthRec.Code != http.StatusOK || !strings.Contains(healthRec.Body.String(), `"stateless":true`) {
		t.Fatalf("unexpected health response: code=%d body=%s", healthRec.Code, healthRec.Body.String())
	}

	// 2. server/discover ve tools/list (SEP-2549) doğrulaması.
	discBody := `{"jsonrpc":"2.0","id":"d1","method":"server/discover","params":{"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"test","version":"1.0"}}}}`
	discRec := sendMCPRequest(router1, token, mcp.MethodServerDiscover, "", discBody)
	if discRec.Code != http.StatusOK || !strings.Contains(discRec.Body.String(), `"multiRoundTrip":true`) {
		t.Fatalf("unexpected server/discover response: %d %s", discRec.Code, discRec.Body.String())
	}

	listBody := `{"jsonrpc":"2.0","id":"l1","method":"tools/list","params":{"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"test","version":"1.0"}}}}`
	listRec := sendMCPRequest(router2, token, mcp.MethodToolsList, "", listBody)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"ttlMs":300000`) {
		t.Fatalf("unexpected tools/list response: %d %s", listRec.Code, listRec.Body.String())
	}

	// 3. tools/call: Router #1 üzerinde olay açıp Router #2 üzerinden okuma.
	createBody := `{
		"jsonrpc":"2.0","id":"c1","method":"tools/call",
		"params":{
			"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"test","version":"1.0"}},
			"name":"incident_context_manager",
			"arguments":{"action":"create","incident_id":"INC-777","title":"ALB Latency"}
		}
	}`
	createRec := sendMCPRequest(router1, token, mcp.MethodToolsCall, mcp.ToolIncidentContextManager, createBody)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create incident failed: %d %s", createRec.Code, createRec.Body.String())
	}

	getBody := `{
		"jsonrpc":"2.0","id":"g1","method":"tools/call",
		"params":{
			"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"test","version":"1.0"}},
			"name":"incident_context_manager",
			"arguments":{"action":"get","incident_id":"INC-777"}
		}
	}`
	getRec := sendMCPRequest(router2, token, mcp.MethodToolsCall, mcp.ToolIncidentContextManager, getBody)
	if getRec.Code != http.StatusOK || !strings.Contains(getRec.Body.String(), "ALB Latency") {
		t.Fatalf("get incident on router2 failed: %d %s", getRec.Code, getRec.Body.String())
	}

	// 4. tools/call: live_telemetry_diagnostic SSE akışı.
	sseBody := `{
		"jsonrpc":"2.0","id":"s1","method":"tools/call",
		"params":{
			"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"test","version":"1.0"},"progressToken":"p1"},
			"name":"live_telemetry_diagnostic",
			"arguments":{"incident_id":"INC-777","target_service":"payment-svc"}
		}
	}`
	sseRec := sendMCPRequest(router1, token, mcp.MethodToolsCall, mcp.ToolLiveTelemetryDiagnostic, sseBody)
	if sseRec.Code != http.StatusOK || sseRec.Header().Get("Content-Type") != toolsContentTypeSSE {
		t.Fatalf("expected SSE stream, got code=%d ct=%s", sseRec.Code, sseRec.Header().Get("Content-Type"))
	}
	if !strings.Contains(sseRec.Body.String(), `"progress":100`) {
		t.Fatalf("expected 100%% progress in SSE body, got: %s", sseRec.Body.String())
	}

	// 5. Edge case: Bilinmeyen araç çağrısı 404 Not Found (-32601) dönmeli.
	unknownToolBody := `{
		"jsonrpc":"2.0","id":"u1","method":"tools/call",
		"params":{
			"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"test","version":"1.0"}},
			"name":"non_existent_tool",
			"arguments":{}
		}
	}`
	unknownRec := sendMCPRequest(router1, token, mcp.MethodToolsCall, "non_existent_tool", unknownToolBody)
	if unknownRec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown tool, got %d: %s", unknownRec.Code, unknownRec.Body.String())
	}
}

const toolsContentTypeSSE = "text/event-stream"

func sendMCPRequest(h http.Handler, token, mcpMethod, mcpName, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, EndpointMCP, strings.NewReader(body))
	req.Header.Set("Content-Type", middleware.ContentTypeJSON)
	req.Header.Set(middleware.HeaderAuthorization, middleware.BearerPrefix+token)
	if mcpMethod != "" {
		req.Header.Set(mcp.HeaderMCPMethod, mcpMethod)
	}
	if mcpName != "" {
		req.Header.Set(mcp.HeaderMCPName, mcpName)
	}
	var resp map[string]any
	_ = json.Unmarshal([]byte(body), &resp)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
