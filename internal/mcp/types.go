package mcp

import (
	"encoding/json"
	"fmt"
)

// Protokol ve JSON-RPC 2.0 sabitleri.
const (
	JSONRPCVersion  = "2.0"
	ProtocolVersion = "2026-07-28"
)

// Desteklenen Stateless MCP metod isimleri ve yasaklanmış legacy metodlar.
const (
	MethodServerDiscover        = "server/discover"
	MethodToolsList             = "tools/list"
	MethodToolsCall             = "tools/call"
	MethodNotificationsProgress = "notifications/progress"

	// Stateless 2026-07-28 spesifikasyonunda yasaklanan stateful metodlar.
	MethodInitialize  = "initialize"
	MethodInitialized = "notifications/initialized"
)

// SEP-2243 L7 yönlendirme başlıkları ve yasaklı oturum başlığı.
const (
	HeaderMCPMethod    = "Mcp-Method"
	HeaderMCPName      = "Mcp-Name"
	HeaderMCPSessionID = "Mcp-Session-Id"
)

// Sistem içerisindeki standart MCP araç isimleri.
const (
	ToolIncidentContextManager  = "incident_context_manager"
	ToolExecuteMitigationPlan   = "execute_mitigation_plan"
	ToolLiveTelemetryDiagnostic = "live_telemetry_diagnostic"
)

// SEP-2322 araç çağrısı durum sabitleri ve içerik tipleri.
const (
	StatusCompleted     = "completed"
	StatusInputRequired = "input_required"
	ContentTypeText     = "text"
)

// JSON-RPC 2.0 standart ve MCP 2026-07-28 özel hata kodları.
const (
	ErrCodeParseError                 = -32700
	ErrCodeInvalidRequest             = -32600
	ErrCodeMethodNotFound             = -32601
	ErrCodeInvalidParams              = -32602
	ErrCodeInternalError              = -32603
	ErrCodeUnsupportedProtocolVersion = -32001
	ErrCodeStatefulHandshakeForbidden = -32002
	ErrCodeContinuationExpired        = -32003
)

// CacheScope SEP-2549 kapsamında araç listesi önbellekleme kapsamını temsil eder.
type CacheScope string

// SEP-2549 geçerli önbellekleme kapsam sabitleri.
const (
	CacheScopePublic  CacheScope = "public"
	CacheScopeTenant  CacheScope = "tenant"
	CacheScopeUser    CacheScope = "user"
	CacheScopeNoStore CacheScope = "no-store"
)

// DefaultToolsListTTLMs araç listesi için varsayılan önbellek süresini (5 dakika) belirtir.
const DefaultToolsListTTLMs int64 = 300000

// JSONRPCRequest istemciden gelen JSON-RPC 2.0 istek zarfını temsil eder.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse sunucudan dönen JSON-RPC 2.0 yanıt zarfını temsil eder.
type JSONRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *JSONRPCError `json:"error,omitempty"`
}

// JSONRPCNotification tek yönlü JSON-RPC 2.0 bildirim zarfını (örn. SSE stream ilerlemesi) temsil eder.
type JSONRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// JSONRPCError JSON-RPC 2.0 standart hata nesnesini temsil eder.
type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error JSONRPCError tipinin standart Go error arayüzünü uygulamasını sağlar.
func (e *JSONRPCError) Error() string {
	return fmt.Sprintf("jsonrpc error (code %d): %s", e.Code, e.Message)
}

// NewJSONRPCResponse başarılı bir JSON-RPC 2.0 yanıtı oluşturur.
func NewJSONRPCResponse(id any, result any) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Result:  result,
	}
}

// NewJSONRPCErrorResponse hata içeren bir JSON-RPC 2.0 yanıtı oluşturur.
func NewJSONRPCErrorResponse(id any, code int, message string, data any) JSONRPCResponse {
	return JSONRPCResponse{
		JSONRPC: JSONRPCVersion,
		ID:      id,
		Error: &JSONRPCError{
			Code:    code,
			Message: message,
			Data:    data,
		},
	}
}

// ClientInfo istemci kimlik ve sürüm bilgilerini taşır.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientCapabilities istemcinin desteklediği Stateless MCP yeteneklerini belirtir.
type ClientCapabilities struct {
	MultiRoundTrip bool           `json:"multiRoundTrip,omitempty"`
	StreamableHTTP bool           `json:"streamableHttp,omitempty"`
	Experimental   map[string]any `json:"experimental,omitempty"`
}

// RequestMeta her Stateless MCP (2026-07-28) isteğinin params._meta alanında zorunlu olan üst veriyi temsil eder.
type RequestMeta struct {
	ProtocolVersion    string             `json:"protocolVersion"`
	ClientInfo         ClientInfo         `json:"clientInfo"`
	ClientCapabilities ClientCapabilities `json:"clientCapabilities,omitempty"`
	ProgressToken      string             `json:"progressToken,omitempty"`
}

// BaseParams tüm MCP metod parametrelerinde bulunan ortak _meta alanını ayrıştırmak için kullanılır.
type BaseParams struct {
	Meta RequestMeta `json:"_meta"`
}

// ServerDiscoverParams server/discover çağrısının parametrelerini temsil eder.
type ServerDiscoverParams struct {
	Meta RequestMeta `json:"_meta"`
}

// ServerInfo sunucunun kimlik, sürüm ve stateless mimari bilgisini taşır.
type ServerInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Stateless bool   `json:"stateless"`
}

// ToolsCapability sunucunun araç listeleme önbellekleme (SEP-2549) desteğini belirtir.
type ToolsCapability struct {
	ListCaching bool `json:"listCaching"`
}

// ServerCapabilities sunucunun desteklediği Stateless MCP 2026-07-28 yeteneklerini temsil eder.
type ServerCapabilities struct {
	Tools          *ToolsCapability `json:"tools,omitempty"`
	MultiRoundTrip bool             `json:"multiRoundTrip"`
	StreamableHTTP bool             `json:"streamableHttp"`
	HeaderRouting  bool             `json:"headerRouting"`
}

// ServerDiscoveryResult server/discover metodunun yanıt yapısını temsil eder.
type ServerDiscoveryResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	Capabilities    ServerCapabilities `json:"capabilities"`
}

// ToolsListParams tools/list çağrısının parametrelerini temsil eder.
type ToolsListParams struct {
	Meta   RequestMeta `json:"_meta"`
	Cursor string      `json:"cursor,omitempty"`
}

// JSONSchemaProperty araç girdi şemasındaki tek bir alanı tanımlar.
type JSONSchemaProperty struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// JSONSchema araçların kabul ettiği parametrelerin JSON Schema tanımını temsil eder.
type JSONSchema struct {
	Type       string                        `json:"type"`
	Properties map[string]JSONSchemaProperty `json:"properties,omitempty"`
	Required   []string                      `json:"required,omitempty"`
}

// ToolDefinition sunucu tarafından sunulan tek bir MCP aracının meta verisini ve şemasını içerir.
type ToolDefinition struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema JSONSchema `json:"inputSchema"`
}

// ToolsListResult SEP-2549 önbellekleme direktiflerini (ttlMs ve cacheScope) içeren tools/list yanıtıdır.
type ToolsListResult struct {
	Tools      []ToolDefinition `json:"tools"`
	TTLMs      int64            `json:"ttlMs"`
	CacheScope CacheScope       `json:"cacheScope"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// ToolCallParams tools/call metodunun parametrelerini ve SEP-2322 continuation_token alanını içerir.
type ToolCallParams struct {
	Meta              RequestMeta     `json:"_meta"`
	Name              string          `json:"name"`
	Arguments         json.RawMessage `json:"arguments,omitempty"`
	ContinuationToken string          `json:"continuation_token,omitempty"`
}

// ContentBlock araç yanıtlarında dönen metin veya yapılandırılmış içerik bloğunu temsil eder.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// RequiredInputField SEP-2322 çok turlu onay akışında istemciden beklenen ek alanı tanımlar.
type RequiredInputField struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
}

// InputRequiredResult SEP-2322 kapsamında bağlantıyı açık tutmadan ek girdi/onay gerektiğinde dönen yanıttır.
type InputRequiredResult struct {
	Status            string               `json:"status"`
	ContinuationToken string               `json:"continuation_token"`
	Reason            string               `json:"reason"`
	RequiredFields    []RequiredInputField `json:"required_fields,omitempty"`
	ExpiresAt         string               `json:"expires_at,omitempty"`
	Metadata          map[string]any       `json:"metadata,omitempty"`
}

// ToolCallResult bir araç çağrısının tamamlanmış sonucunu veya SEP-2322 InputRequiredResult çıktısını taşır.
type ToolCallResult struct {
	Status        string               `json:"status"`
	Content       []ContentBlock       `json:"content,omitempty"`
	IsError       bool                 `json:"isError,omitempty"`
	InputRequired *InputRequiredResult `json:"input_required,omitempty"`
}

// NewTextToolResult tamamlanmış bir metin tabanlı araç yanıtı oluşturur.
func NewTextToolResult(text string, isError bool) ToolCallResult {
	return ToolCallResult{
		Status: StatusCompleted,
		Content: []ContentBlock{
			{
				Type: ContentTypeText,
				Text: text,
			},
		},
		IsError: isError,
	}
}

// NewInputRequiredToolResult SEP-2322 standardına uygun çok turlu onay bekleyen araç yanıtı oluşturur.
func NewInputRequiredToolResult(inputReq InputRequiredResult) ToolCallResult {
	inputReq.Status = StatusInputRequired
	return ToolCallResult{
		Status:        StatusInputRequired,
		InputRequired: &inputReq,
	}
}

// ProgressNotificationParams Streamable HTTP (SSE) üzerinden gönderilen gerçek zamanlı ilerleme bildirimini temsil eder.
type ProgressNotificationParams struct {
	ProgressToken string `json:"progressToken,omitempty"`
	Progress      int    `json:"progress"`
	Total         int    `json:"total"`
	Message       string `json:"message"`
	Stage         string `json:"stage,omitempty"`
}
