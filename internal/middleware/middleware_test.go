package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mcp-incident-commander/internal/mcp"
)

func TestValidateSEP2243Headers(t *testing.T) {
	t.Parallel()

	downstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) == 0 {
			t.Error("expected restored request body in downstream handler")
		}
		w.WriteHeader(http.StatusOK)
	})
	handler := ValidateSEP2243Headers(downstream)

	callBody := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"incident_context_manager"}}`

	// Happy path: Mcp-Method ve Mcp-Name gövdeyle birebir uyumlu
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(callBody))
	req.Header.Set(mcp.HeaderMCPMethod, mcp.MethodToolsCall)
	req.Header.Set(mcp.HeaderMCPName, mcp.ToolIncidentContextManager)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	// Edge cases: yasaklı Mcp-Session-Id, eksik Mcp-Method ve uyumsuz Mcp-Name
	errCases := []struct {
		name   string
		header map[string]string
		body   string
	}{
		{
			name:   "forbidden Mcp-Session-Id header",
			header: map[string]string{mcp.HeaderMCPSessionID: "sess-1", mcp.HeaderMCPMethod: mcp.MethodServerDiscover},
			body:   `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`,
		},
		{
			name:   "mismatched Mcp-Method and body method",
			header: map[string]string{mcp.HeaderMCPMethod: mcp.MethodToolsList},
			body:   `{"jsonrpc":"2.0","id":1,"method":"server/discover"}`,
		},
		{
			name:   "mismatched Mcp-Name for tools/call",
			header: map[string]string{mcp.HeaderMCPMethod: mcp.MethodToolsCall, mcp.HeaderMCPName: mcp.ToolExecuteMitigationPlan},
			body:   callBody,
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(tc.body))
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request, got %d", w.Code)
			}
		})
	}
}

func TestJWTAuthMiddleware(t *testing.T) {
	t.Parallel()

	secret := []byte("test-hmac-secret-key-2026")
	authMW := NewJWTAuthMiddleware(secret)

	var capturedIdentity Identity
	handler := authMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := IdentityFromContext(r.Context())
		if !ok {
			t.Error("expected identity in context")
		}
		capturedIdentity = id
		w.WriteHeader(http.StatusOK)
	}))

	// Happy path: geçerli Bearer JWT ile TenantID ve EngineerID aktarımı
	token, err := GenerateToken(secret, "tenant-finops-01", "eng-sre-42", 5*time.Minute)
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set(HeaderAuthorization, BearerPrefix+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}
	if capturedIdentity.TenantID != "tenant-finops-01" || capturedIdentity.EngineerID != "eng-sre-42" {
		t.Fatalf("unexpected identity: %+v", capturedIdentity)
	}

	// Edge case: süresi dolmuş veya yanlış imzalı token reddi (401 Unauthorized)
	expiredToken, _ := GenerateToken(secret, "tenant-finops-01", "eng-sre-42", -1*time.Minute)
	badReq := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	badReq.Header.Set(HeaderAuthorization, BearerPrefix+expiredToken)
	badRec := httptest.NewRecorder()
	handler.ServeHTTP(badRec, badReq)

	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for expired token, got %d", badRec.Code)
	}
}
