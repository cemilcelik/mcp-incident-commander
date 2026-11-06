package mcpclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/tools"
)

// İstemci varsayılan yapılandırma sabitleri.
const (
	DefaultClientName    = "mcp-incident-cli"
	DefaultClientVersion = "1.0.0"
	DefaultHTTPTimeout   = 60 * time.Second
)

// İstemci standart hata tanımları.
var (
	ErrEmptyEndpoint    = errors.New("mcp server endpoint cannot be empty")
	ErrEmptyBearerToken = errors.New("bearer token cannot be empty")
	ErrEmptyToolName    = errors.New("tool name cannot be empty")
	ErrMissingSSEFinal  = errors.New("SSE stream ended without a final JSON-RPC response message")
)

// ApprovalHandler SEP-2322 InputRequiredResult alındığında ikinci tur onay kararını üreten geri çağırım fonksiyonudur.
type ApprovalHandler func(ctx context.Context, inputReq mcp.InputRequiredResult) (approved bool, comment string, err error)

// ProgressHandler Streamable HTTP (SSE) üzerinden gelen ilerleme bildirimlerini anlık işleyen geri çağırım fonksiyonudur.
type ProgressHandler func(notif mcp.ProgressNotificationParams)

// Config Stateless MCP Go istemcisinin yapılandırma parametrelerini taşır.
type Config struct {
	Endpoint        string
	BearerToken     string
	ClientName      string
	ClientVersion   string
	HTTPClient      *http.Client
	ApprovalHandler ApprovalHandler
	ProgressHandler ProgressHandler
}

// Client initialize el sıkışması ve oturum kimliği kullanmayan Stateless MCP 2026-07-28 istemcisidir.
type Client struct {
	endpoint        string
	bearerToken     string
	clientInfo      mcp.ClientInfo
	httpClient      *http.Client
	approvalHandler ApprovalHandler
	progressHandler ProgressHandler
	reqCounter      atomic.Uint64

	cacheMu         sync.RWMutex
	cachedToolsList *mcp.ToolsListResult
	cacheExpiresAt  time.Time
}

// New yapılandırılmış yeni bir Stateless MCP Client örneği oluşturur.
func New(cfg Config) (*Client, error) {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		return nil, ErrEmptyEndpoint
	}
	token := strings.TrimSpace(cfg.BearerToken)
	if token == "" {
		return nil, ErrEmptyBearerToken
	}

	clientName := strings.TrimSpace(cfg.ClientName)
	if clientName == "" {
		clientName = DefaultClientName
	}
	clientVersion := strings.TrimSpace(cfg.ClientVersion)
	if clientVersion == "" {
		clientVersion = DefaultClientVersion
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: DefaultHTTPTimeout}
	}

	return &Client{
		endpoint:    endpoint,
		bearerToken: token,
		clientInfo: mcp.ClientInfo{
			Name:    clientName,
			Version: clientVersion,
		},
		httpClient:      httpClient,
		approvalHandler: cfg.ApprovalHandler,
		progressHandler: cfg.ProgressHandler,
	}, nil
}

func (c *Client) nextRequestID() string {
	id := c.reqCounter.Add(1)
	return fmt.Sprintf("req-%d", id)
}

func (c *Client) buildMeta(progressToken string) mcp.RequestMeta {
	return mcp.RequestMeta{
		ProtocolVersion: mcp.ProtocolVersion,
		ClientInfo:      c.clientInfo,
		ClientCapabilities: mcp.ClientCapabilities{
			MultiRoundTrip: true,
			StreamableHTTP: true,
		},
		ProgressToken: progressToken,
	}
}

// Discover sunucunun Stateless MCP 2026-07-28 yeteneklerini server/discover metoduyla sorgular.
func (c *Client) Discover(ctx context.Context) (*mcp.ServerDiscoveryResult, error) {
	params := mcp.ServerDiscoverParams{
		Meta: c.buildMeta(""),
	}
	var out mcp.ServerDiscoveryResult
	if err := c.doJSONRPC(ctx, mcp.MethodServerDiscover, "", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListTools araç listesini sorgular ve SEP-2549 ttlMs / cacheScope kurallarına göre önbellekler.
func (c *Client) ListTools(ctx context.Context) (*mcp.ToolsListResult, error) {
	c.cacheMu.RLock()
	if c.cachedToolsList != nil && time.Now().Before(c.cacheExpiresAt) {
		cached := *c.cachedToolsList
		c.cacheMu.RUnlock()
		return &cached, nil
	}
	c.cacheMu.RUnlock()

	params := mcp.ToolsListParams{
		Meta: c.buildMeta(""),
	}
	var out mcp.ToolsListResult
	if err := c.doJSONRPC(ctx, mcp.MethodToolsList, "", params, &out); err != nil {
		return nil, err
	}

	if out.TTLMs > 0 && out.CacheScope != mcp.CacheScopeNoStore {
		c.cacheMu.Lock()
		copied := out
		c.cachedToolsList = &copied
		c.cacheExpiresAt = time.Now().Add(time.Duration(out.TTLMs) * time.Millisecond)
		c.cacheMu.Unlock()
	}

	return &out, nil
}

// CallTool belirtilen aracı çağırır; SSE stream yanıtlarını ayrıştırır ve SEP-2322 onay gerekirse 2. turu yürütür.
func (c *Client) CallTool(ctx context.Context, toolName string, arguments any) (*mcp.ToolCallResult, error) {
	res, err := c.CallToolWithContinuation(ctx, toolName, "", arguments)
	if err != nil {
		return nil, err
	}

	// SEP-2322 Multi-Round-Trip: InputRequiredResult döndüyse ve ApprovalHandler tanımlıysa 2. tur isteği gönder.
	if res.Status == mcp.StatusInputRequired && res.InputRequired != nil && c.approvalHandler != nil {
		approved, comment, err := c.approvalHandler(ctx, *res.InputRequired)
		if err != nil {
			return nil, fmt.Errorf("approval handler failed: %w", err)
		}

		followUpArgs := map[string]any{
			"approved":         approved,
			"approver_comment": comment,
		}
		return c.CallToolWithContinuation(ctx, toolName, res.InputRequired.ContinuationToken, followUpArgs)
	}

	return res, nil
}

// CallToolWithContinuation SEP-2322 continuation_token ile doğrudan araç çağrısı yapar.
func (c *Client) CallToolWithContinuation(
	ctx context.Context,
	toolName string,
	continuationToken string,
	arguments any,
) (*mcp.ToolCallResult, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return nil, ErrEmptyToolName
	}

	var rawArgs json.RawMessage
	if arguments != nil {
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal tool arguments: %w", err)
		}
		rawArgs = encoded
	} else {
		rawArgs = json.RawMessage(`{}`)
	}

	reqID := c.nextRequestID()
	params := mcp.ToolCallParams{
		Meta:              c.buildMeta("prog-" + reqID),
		Name:              toolName,
		Arguments:         rawArgs,
		ContinuationToken: strings.TrimSpace(continuationToken),
	}

	resp, err := c.sendHTTPRequest(ctx, reqID, mcp.MethodToolsCall, toolName, params)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, tools.ContentTypeEventStream) {
		return c.readSSEToolResult(resp.Body)
	}

	var toolRes mcp.ToolCallResult
	if err := decodeJSONRPCResponse(resp.Body, &toolRes); err != nil {
		return nil, err
	}
	return &toolRes, nil
}

func (c *Client) doJSONRPC(ctx context.Context, method, toolName string, params any, out any) error {
	reqID := c.nextRequestID()
	resp, err := c.sendHTTPRequest(ctx, reqID, method, toolName, params)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return decodeJSONRPCResponse(resp.Body, out)
}

func (c *Client) sendHTTPRequest(ctx context.Context, reqID, method, toolName string, params any) (*http.Response, error) {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request params: %w", err)
	}

	rpcReq := mcp.JSONRPCRequest{
		JSONRPC: mcp.JSONRPCVersion,
		ID:      reqID,
		Method:  method,
		Params:  rawParams,
	}

	bodyBytes, err := json.Marshal(rpcReq)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize JSON-RPC envelope: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	// SEP-2243 L7 başlıkları ve Stateless Bearer JWT başlığı (Mcp-Session-Id kesinlikle gönderilmez).
	httpReq.Header.Set("Content-Type", middleware.ContentTypeJSON)
	httpReq.Header.Set(middleware.HeaderAuthorization, middleware.BearerPrefix+c.bearerToken)
	httpReq.Header.Set(mcp.HeaderMCPMethod, method)
	if toolName != "" {
		httpReq.Header.Set(mcp.HeaderMCPName, toolName)
	}

	return c.httpClient.Do(httpReq)
}

func (c *Client) readSSEToolResult(body io.Reader) (*mcp.ToolCallResult, error) {
	scanner := bufio.NewScanner(body)
	var finalResult *mcp.ToolCallResult

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")

		if strings.Contains(payload, `"method":"`+mcp.MethodNotificationsProgress+`"`) {
			if c.progressHandler != nil {
				var notif struct {
					Params mcp.ProgressNotificationParams `json:"params"`
				}
				if err := json.Unmarshal([]byte(payload), &notif); err == nil {
					c.progressHandler(notif.Params)
				}
			}
			continue
		}

		var res mcp.ToolCallResult
		if err := decodeJSONRPCResponse(strings.NewReader(payload), &res); err != nil {
			return nil, err
		}
		finalResult = &res
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading SSE stream: %w", err)
	}
	if finalResult == nil {
		return nil, ErrMissingSSEFinal
	}
	return finalResult, nil
}

func decodeJSONRPCResponse(r io.Reader, target any) error {
	var env struct {
		JSONRPC string            `json:"jsonrpc"`
		ID      any               `json:"id"`
		Result  json.RawMessage   `json:"result"`
		Error   *mcp.JSONRPCError `json:"error"`
	}
	if err := json.NewDecoder(r).Decode(&env); err != nil {
		return fmt.Errorf("failed to decode JSON-RPC response: %w", err)
	}
	if env.Error != nil {
		return env.Error
	}
	if target == nil || len(env.Result) == 0 {
		return nil
	}
	return json.Unmarshal(env.Result, target)
}
