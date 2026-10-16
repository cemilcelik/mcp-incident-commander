package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestParseAndValidateRequest_Success(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"jsonrpc": "2.0",
		"id": "req-101",
		"method": "server/discover",
		"params": {
			"_meta": {
				"protocolVersion": "2026-07-28",
				"clientInfo": {
					"name": "incident-cli",
					"version": "1.0.0"
				},
				"clientCapabilities": {
					"multiRoundTrip": true,
					"streamableHttp": true
				}
			}
		}
	}`)

	req, meta, rpcErr := ParseAndValidateRequest(payload)
	if rpcErr != nil {
		t.Fatalf("expected no error, got: %v", rpcErr)
	}
	if req.Method != MethodServerDiscover {
		t.Errorf("expected method %q, got %q", MethodServerDiscover, req.Method)
	}
	if req.ID != "req-101" {
		t.Errorf("expected id 'req-101', got %v", req.ID)
	}
	if meta.ProtocolVersion != ProtocolVersion {
		t.Errorf("expected protocolVersion %q, got %q", ProtocolVersion, meta.ProtocolVersion)
	}
	if meta.ClientInfo.Name != "incident-cli" {
		t.Errorf("expected client name 'incident-cli', got %q", meta.ClientInfo.Name)
	}
	if !meta.ClientCapabilities.MultiRoundTrip || !meta.ClientCapabilities.StreamableHTTP {
		t.Errorf("expected client capabilities multiRoundTrip=true and streamableHttp=true")
	}
}

func TestParseAndValidateRequest_ValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		payload      string
		expectedCode int
		expectedMsg  string
	}{
		{
			name:         "empty payload",
			payload:      "   ",
			expectedCode: ErrCodeParseError,
			expectedMsg:  errMsgEmptyPayload,
		},
		{
			name:         "malformed json",
			payload:      `{"jsonrpc": "2.0", "id": 1`,
			expectedCode: ErrCodeParseError,
			expectedMsg:  errMsgInvalidJSON,
		},
		{
			name:         "invalid jsonrpc version",
			payload:      `{"jsonrpc": "1.0", "id": 1, "method": "server/discover"}`,
			expectedCode: ErrCodeInvalidRequest,
			expectedMsg:  errMsgInvalidJSONRPCVersion,
		},
		{
			name:         "missing method",
			payload:      `{"jsonrpc": "2.0", "id": 1}`,
			expectedCode: ErrCodeInvalidRequest,
			expectedMsg:  errMsgMissingMethod,
		},
		{
			name: "forbidden initialize handshake",
			payload: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "initialize",
				"params": {"_meta": {"protocolVersion": "2026-07-28", "clientInfo": {"name": "c", "version": "1"}}}
			}`,
			expectedCode: ErrCodeStatefulHandshakeForbidden,
			expectedMsg:  "forbidden in Stateless MCP 2026-07-28",
		},
		{
			name: "forbidden initialized notification",
			payload: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "notifications/initialized"
			}`,
			expectedCode: ErrCodeStatefulHandshakeForbidden,
			expectedMsg:  "forbidden in Stateless MCP 2026-07-28",
		},
		{
			name:         "missing request id",
			payload:      `{"jsonrpc": "2.0", "method": "server/discover"}`,
			expectedCode: ErrCodeInvalidRequest,
			expectedMsg:  errMsgMissingRequestID,
		},
		{
			name:         "missing params",
			payload:      `{"jsonrpc": "2.0", "id": 1, "method": "server/discover"}`,
			expectedCode: ErrCodeInvalidParams,
			expectedMsg:  errMsgMissingParams,
		},
		{
			name: "missing protocolVersion in _meta",
			payload: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "tools/list",
				"params": {"_meta": {"clientInfo": {"name": "cli", "version": "1.0"}}}
			}`,
			expectedCode: ErrCodeInvalidParams,
			expectedMsg:  errMsgMissingProtocolVersion,
		},
		{
			name: "unsupported legacy protocolVersion",
			payload: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "tools/list",
				"params": {"_meta": {"protocolVersion": "2024-11-05", "clientInfo": {"name": "cli", "version": "1.0"}}}
			}`,
			expectedCode: ErrCodeUnsupportedProtocolVersion,
			expectedMsg:  "unsupported protocolVersion",
		},
		{
			name: "missing clientInfo name",
			payload: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "tools/list",
				"params": {"_meta": {"protocolVersion": "2026-07-28", "clientInfo": {"version": "1.0"}}}
			}`,
			expectedCode: ErrCodeInvalidParams,
			expectedMsg:  errMsgMissingClientName,
		},
		{
			name: "missing clientInfo version",
			payload: `{
				"jsonrpc": "2.0",
				"id": 1,
				"method": "tools/list",
				"params": {"_meta": {"protocolVersion": "2026-07-28", "clientInfo": {"name": "cli"}}}
			}`,
			expectedCode: ErrCodeInvalidParams,
			expectedMsg:  errMsgMissingClientVersion,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, _, rpcErr := ParseAndValidateRequest([]byte(tc.payload))
			if rpcErr == nil {
				t.Fatalf("expected error with code %d, got nil", tc.expectedCode)
			}
			if rpcErr.Code != tc.expectedCode {
				t.Errorf("expected error code %d, got %d (msg: %s)", tc.expectedCode, rpcErr.Code, rpcErr.Message)
			}
			if !strings.Contains(rpcErr.Message, tc.expectedMsg) {
				t.Errorf("expected error message containing %q, got %q", tc.expectedMsg, rpcErr.Message)
			}
			if !strings.Contains(rpcErr.Error(), tc.expectedMsg) {
				t.Errorf("expected Error() string containing %q, got %q", tc.expectedMsg, rpcErr.Error())
			}
		})
	}
}

func TestParseToolCallParams_SuccessAndContinuation(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{
		"_meta": {
			"protocolVersion": "2026-07-28",
			"clientInfo": {"name": "incident-cli", "version": "1.0.0"}
		},
		"name": "execute_mitigation_plan",
		"continuation_token": "cont-token-999",
		"arguments": {"approved": true}
	}`)

	params, rpcErr := ParseToolCallParams(raw)
	if rpcErr != nil {
		t.Fatalf("expected no error, got: %v", rpcErr)
	}
	if params.Name != ToolExecuteMitigationPlan {
		t.Errorf("expected tool name %q, got %q", ToolExecuteMitigationPlan, params.Name)
	}
	if params.ContinuationToken != "cont-token-999" {
		t.Errorf("expected continuation_token 'cont-token-999', got %q", params.ContinuationToken)
	}

	// Eksik araç adı senaryosu (Edge case).
	missingNameRaw := json.RawMessage(`{
		"_meta": {
			"protocolVersion": "2026-07-28",
			"clientInfo": {"name": "incident-cli", "version": "1.0.0"}
		}
	}`)
	_, errMissingName := ParseToolCallParams(missingNameRaw)
	if errMissingName == nil || errMissingName.Code != ErrCodeInvalidParams {
		t.Fatalf("expected ErrCodeInvalidParams for missing tool name, got: %v", errMissingName)
	}
}

func TestServerDiscoverySerialization(t *testing.T) {
	t.Parallel()

	discovery := DefaultServerDiscovery("mcp-incident-commander", "1.0.0")
	resp := NewJSONRPCResponse("disc-1", discovery)

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal discovery response: %v", err)
	}

	var decoded struct {
		JSONRPC string                `json:"jsonrpc"`
		ID      string                `json:"id"`
		Result  ServerDiscoveryResult `json:"result"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("failed to unmarshal discovery response: %v", err)
	}

	if decoded.Result.ProtocolVersion != ProtocolVersion {
		t.Errorf("expected protocolVersion %q, got %q", ProtocolVersion, decoded.Result.ProtocolVersion)
	}
	if !decoded.Result.ServerInfo.Stateless {
		t.Errorf("expected serverInfo.stateless to be true")
	}
	if !decoded.Result.Capabilities.MultiRoundTrip || !decoded.Result.Capabilities.StreamableHTTP || !decoded.Result.Capabilities.HeaderRouting {
		t.Errorf("expected all stateless capabilities to be enabled")
	}
	if decoded.Result.Capabilities.Tools == nil || !decoded.Result.Capabilities.Tools.ListCaching {
		t.Errorf("expected tools.listCaching capability to be true")
	}
}

func TestSEP2549ToolsListValidationAndSerialization(t *testing.T) {
	t.Parallel()

	validList := ToolsListResult{
		Tools: []ToolDefinition{
			{
				Name:        ToolIncidentContextManager,
				Description: "Manages stateful incident context across stateless Lambda instances",
				InputSchema: JSONSchema{
					Type: "object",
					Properties: map[string]JSONSchemaProperty{
						"action": {Type: "string", Enum: []string{"create", "append_note", "get"}},
					},
					Required: []string{"action"},
				},
			},
		},
		TTLMs:      DefaultToolsListTTLMs,
		CacheScope: CacheScopeTenant,
	}

	if err := ValidateToolsListResult(validList); err != nil {
		t.Fatalf("expected valid SEP-2549 ToolsListResult, got error: %v", err)
	}

	data, err := json.Marshal(validList)
	if err != nil {
		t.Fatalf("failed to marshal ToolsListResult: %v", err)
	}
	if !strings.Contains(string(data), `"ttlMs":300000`) || !strings.Contains(string(data), `"cacheScope":"tenant"`) {
		t.Errorf("serialized JSON missing SEP-2549 fields: %s", string(data))
	}

	// Negatif TTL hata testi.
	invalidTTL := validList
	invalidTTL.TTLMs = -10
	if err := ValidateToolsListResult(invalidTTL); !errors.Is(err, ErrNegativeTTL) {
		t.Errorf("expected ErrNegativeTTL, got %v", err)
	}

	// Geçersiz CacheScope hata testi.
	invalidScope := validList
	invalidScope.CacheScope = "invalid_scope"
	if err := ValidateToolsListResult(invalidScope); !errors.Is(err, ErrInvalidCacheScope) {
		t.Errorf("expected ErrInvalidCacheScope, got %v", err)
	}
}

func TestSEP2322InputRequiredValidationAndSerialization(t *testing.T) {
	t.Parallel()

	inputReq := InputRequiredResult{
		ContinuationToken: "ct_89f1e2a",
		Reason:            "Mitigation plan estimated cost ($1,250) exceeds $500 threshold and risk level is high",
		RequiredFields: []RequiredInputField{
			{
				Name:        "approved",
				Type:        "boolean",
				Description: "Explicit FinOps & Incident Commander confirmation",
				Required:    true,
			},
		},
		ExpiresAt: "2026-10-16T13:00:00Z",
		Metadata: map[string]any{
			"estimated_cost_usd": 1250,
			"risk_level":         "high",
		},
	}

	toolResult := NewInputRequiredToolResult(inputReq)
	if toolResult.Status != StatusInputRequired || toolResult.InputRequired == nil {
		t.Fatalf("expected toolResult status %q and non-nil InputRequired", StatusInputRequired)
	}

	if err := ValidateInputRequiredResult(*toolResult.InputRequired); err != nil {
		t.Fatalf("expected valid InputRequiredResult, got: %v", err)
	}

	// Eksik continuation_token testi.
	missingToken := *toolResult.InputRequired
	missingToken.ContinuationToken = ""
	if err := ValidateInputRequiredResult(missingToken); !errors.Is(err, ErrMissingContinuationToken) {
		t.Errorf("expected ErrMissingContinuationToken, got %v", err)
	}

	// Eksik reason testi.
	missingReason := *toolResult.InputRequired
	missingReason.Reason = ""
	if err := ValidateInputRequiredResult(missingReason); !errors.Is(err, ErrMissingInputReason) {
		t.Errorf("expected ErrMissingInputReason, got %v", err)
	}
}

func TestNewTextToolResultAndErrorResponse(t *testing.T) {
	t.Parallel()

	res := NewTextToolResult("Incident INC-42 created", false)
	if res.Status != StatusCompleted || len(res.Content) != 1 || res.Content[0].Text != "Incident INC-42 created" {
		t.Errorf("unexpected text tool result: %+v", res)
	}

	errResp := NewJSONRPCErrorResponse("req-9", ErrCodeInvalidRequest, "bad header", map[string]string{"header": HeaderMCPMethod})
	if errResp.Error == nil || errResp.Error.Code != ErrCodeInvalidRequest {
		t.Errorf("unexpected error response: %+v", errResp)
	}
}
