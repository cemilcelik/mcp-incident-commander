package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Doğrulama hata mesajı sabitleri.
const (
	errMsgEmptyPayload              = "request payload cannot be empty"
	errMsgInvalidJSON               = "failed to parse JSON-RPC 2.0 request payload"
	errMsgInvalidJSONRPCVersion     = "invalid jsonrpc version: must be '2.0'"
	errMsgMissingRequestID          = "missing request id: stateless MCP requests require a valid id"
	errMsgMissingMethod             = "missing method name in JSON-RPC request"
	errMsgStatefulHandshakeRejected = "stateful handshake ('initialize') is forbidden in Stateless MCP 2026-07-28; send self-contained requests with params._meta or call 'server/discover'"
	errMsgMissingParams             = "missing params object: params._meta is mandatory in Stateless MCP 2026-07-28"
	errMsgInvalidParamsJSON         = "failed to parse request params object"
	errMsgMissingProtocolVersion    = "missing params._meta.protocolVersion"
	errMsgMissingClientName         = "missing params._meta.clientInfo.name"
	errMsgMissingClientVersion      = "missing params._meta.clientInfo.version"
	errMsgMissingToolName           = "missing tool name in tools/call params"
)

// Standart Go hata tanımları (SEP-2549 ve SEP-2322 yapı doğrulamaları için).
var (
	ErrNegativeTTL               = errors.New("ttlMs cannot be negative")
	ErrInvalidCacheScope         = errors.New("invalid cacheScope value for SEP-2549")
	ErrInvalidInputRequiredState = errors.New("input_required status must be 'input_required'")
	ErrMissingContinuationToken  = errors.New("continuation_token is required for SEP-2322 InputRequiredResult")
	ErrMissingInputReason        = errors.New("reason is required for SEP-2322 InputRequiredResult")
)

// ParseAndValidateRequest ham JSON verisini ayrıştırır ve Stateless MCP 2026-07-28 zarf kurallarını doğrular.
func ParseAndValidateRequest(payload []byte) (*JSONRPCRequest, *RequestMeta, *JSONRPCError) {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return nil, nil, &JSONRPCError{
			Code:    ErrCodeParseError,
			Message: errMsgEmptyPayload,
		}
	}

	var req JSONRPCRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		return nil, nil, &JSONRPCError{
			Code:    ErrCodeParseError,
			Message: errMsgInvalidJSON,
			Data:    err.Error(),
		}
	}

	if req.JSONRPC != JSONRPCVersion {
		return &req, nil, &JSONRPCError{
			Code:    ErrCodeInvalidRequest,
			Message: errMsgInvalidJSONRPCVersion,
		}
	}

	method := strings.TrimSpace(req.Method)
	if method == "" {
		return &req, nil, &JSONRPCError{
			Code:    ErrCodeInvalidRequest,
			Message: errMsgMissingMethod,
		}
	}

	// Stateless MCP 2026-07-28 protokolünde initialize el sıkışması kesinlikle yasaktır.
	if method == MethodInitialize || method == MethodInitialized {
		return &req, nil, &JSONRPCError{
			Code:    ErrCodeStatefulHandshakeForbidden,
			Message: errMsgStatefulHandshakeRejected,
		}
	}

	if req.ID == nil || req.ID == "" {
		return &req, nil, &JSONRPCError{
			Code:    ErrCodeInvalidRequest,
			Message: errMsgMissingRequestID,
		}
	}

	if len(req.Params) == 0 || string(req.Params) == "null" {
		return &req, nil, &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgMissingParams,
		}
	}

	var base BaseParams
	if err := json.Unmarshal(req.Params, &base); err != nil {
		return &req, nil, &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgInvalidParamsJSON,
			Data:    err.Error(),
		}
	}

	if rpcErr := ValidateMeta(base.Meta); rpcErr != nil {
		return &req, nil, rpcErr
	}

	return &req, &base.Meta, nil
}

// ValidateMeta her istekte zorunlu olan _meta alanının 2026-07-28 protokol kurallarına uygunluğunu denetler.
func ValidateMeta(meta RequestMeta) *JSONRPCError {
	if strings.TrimSpace(meta.ProtocolVersion) == "" {
		return &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgMissingProtocolVersion,
		}
	}

	if meta.ProtocolVersion != ProtocolVersion {
		return &JSONRPCError{
			Code:    ErrCodeUnsupportedProtocolVersion,
			Message: fmt.Sprintf("unsupported protocolVersion %q: server strictly requires %q", meta.ProtocolVersion, ProtocolVersion),
		}
	}

	if strings.TrimSpace(meta.ClientInfo.Name) == "" {
		return &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgMissingClientName,
		}
	}

	if strings.TrimSpace(meta.ClientInfo.Version) == "" {
		return &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgMissingClientVersion,
		}
	}

	return nil
}

// ParseToolCallParams tools/call parametrelerini ayrıştırır ve araç çağrısı kurallarını doğrular.
func ParseToolCallParams(rawParams json.RawMessage) (*ToolCallParams, *JSONRPCError) {
	if len(rawParams) == 0 || string(rawParams) == "null" {
		return nil, &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgMissingParams,
		}
	}

	var params ToolCallParams
	if err := json.Unmarshal(rawParams, &params); err != nil {
		return nil, &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgInvalidParamsJSON,
			Data:    err.Error(),
		}
	}

	if rpcErr := ValidateMeta(params.Meta); rpcErr != nil {
		return nil, rpcErr
	}

	if strings.TrimSpace(params.Name) == "" {
		return nil, &JSONRPCError{
			Code:    ErrCodeInvalidParams,
			Message: errMsgMissingToolName,
		}
	}

	return &params, nil
}

// ValidateToolsListResult SEP-2549 kapsamında dönen araç listesi önbellek parametrelerini doğrular.
func ValidateToolsListResult(res ToolsListResult) error {
	if res.TTLMs < 0 {
		return ErrNegativeTTL
	}

	switch res.CacheScope {
	case CacheScopePublic, CacheScopeTenant, CacheScopeUser, CacheScopeNoStore:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidCacheScope, res.CacheScope)
	}
}

// ValidateInputRequiredResult SEP-2322 çok turlu onay yanıtının zorunlu alanlarını doğrular.
func ValidateInputRequiredResult(res InputRequiredResult) error {
	if res.Status != StatusInputRequired {
		return ErrInvalidInputRequiredState
	}

	if strings.TrimSpace(res.ContinuationToken) == "" {
		return ErrMissingContinuationToken
	}

	if strings.TrimSpace(res.Reason) == "" {
		return ErrMissingInputReason
	}

	return nil
}

// DefaultServerDiscovery sunucunun Stateless MCP 2026-07-28 yetenek keşif yanıtını üretir.
func DefaultServerDiscovery(serverName, serverVersion string) ServerDiscoveryResult {
	return ServerDiscoveryResult{
		ProtocolVersion: ProtocolVersion,
		ServerInfo: ServerInfo{
			Name:      serverName,
			Version:   serverVersion,
			Stateless: true,
		},
		Capabilities: ServerCapabilities{
			Tools: &ToolsCapability{
				ListCaching: true,
			},
			MultiRoundTrip: true,
			StreamableHTTP: true,
			HeaderRouting:  true,
		},
	}
}
