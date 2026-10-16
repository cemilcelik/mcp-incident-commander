package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseAndValidateRequest(t *testing.T) {
	t.Parallel()

	// Happy path: geçerli 2026-07-28 isteği
	validPayload := []byte(`{
		"jsonrpc": "2.0",
		"id": "req-101",
		"method": "server/discover",
		"params": {
			"_meta": {
				"protocolVersion": "2026-07-28",
				"clientInfo": {"name": "incident-cli", "version": "1.0.0"},
				"clientCapabilities": {"multiRoundTrip": true, "streamableHttp": true}
			}
		}
	}`)

	req, meta, rpcErr := ParseAndValidateRequest(validPayload)
	if rpcErr != nil {
		t.Fatalf("expected no error, got: %v", rpcErr)
	}
	if req.Method != MethodServerDiscover || meta.ProtocolVersion != ProtocolVersion {
		t.Fatalf("unexpected parsed request or meta: %+v, %+v", req, meta)
	}

	// Edge cases: yasaklı initialize el sıkışması ve desteklenmeyen protokol sürümü
	errCases := []struct {
		name    string
		payload string
		code    int
	}{
		{
			name:    "forbidden initialize handshake",
			payload: `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"_meta":{"protocolVersion":"2026-07-28","clientInfo":{"name":"c","version":"1"}}}}`,
			code:    ErrCodeStatefulHandshakeForbidden,
		},
		{
			name:    "unsupported protocol version",
			payload: `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"protocolVersion":"2024-11-05","clientInfo":{"name":"c","version":"1"}}}}`,
			code:    ErrCodeUnsupportedProtocolVersion,
		},
	}

	for _, tc := range errCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ParseAndValidateRequest([]byte(tc.payload))
			if err == nil || err.Code != tc.code {
				t.Fatalf("expected error code %d, got: %v", tc.code, err)
			}
		})
	}
}

func TestSEP2549AndSEP2322Validation(t *testing.T) {
	t.Parallel()

	// SEP-2549: geçerli ve geçersiz önbellek kapsamı
	validList := ToolsListResult{TTLMs: DefaultToolsListTTLMs, CacheScope: CacheScopeTenant}
	if err := ValidateToolsListResult(validList); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := ValidateToolsListResult(ToolsListResult{TTLMs: -1, CacheScope: CacheScopeTenant}); !errors.Is(err, ErrNegativeTTL) {
		t.Fatalf("expected ErrNegativeTTL, got: %v", err)
	}

	// SEP-2322: InputRequiredResult ve continuation_token ayrıştırma
	toolRes := NewInputRequiredToolResult(InputRequiredResult{
		ContinuationToken: "ct_89f1e2a",
		Reason:            "High cost mitigation requires approval",
	})
	if err := ValidateInputRequiredResult(*toolRes.InputRequired); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rawCall := json.RawMessage(`{
		"_meta": {"protocolVersion": "2026-07-28", "clientInfo": {"name": "cli", "version": "1.0"}},
		"name": "execute_mitigation_plan",
		"continuation_token": "ct_89f1e2a"
	}`)
	callParams, rpcErr := ParseToolCallParams(rawCall)
	if rpcErr != nil || callParams.ContinuationToken != "ct_89f1e2a" {
		t.Fatalf("failed to parse continuation_token: %v", rpcErr)
	}

	// Discovery yanıt serileştirme kontrolü
	discData, err := json.Marshal(DefaultServerDiscovery("mcp-incident-commander", "1.0.0"))
	if err != nil || !strings.Contains(string(discData), `"stateless":true`) {
		t.Fatalf("unexpected discovery serialization: %s, err: %v", string(discData), err)
	}
}
