package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/store"
	"mcp-incident-commander/internal/tools"
)

// Sunucu varsayılan yapılandırma ve uç nokta sabitleri.
const (
	DefaultServerName    = "mcp-incident-commander"
	DefaultServerVersion = "1.0.0"
	EndpointMCP          = "/mcp"
	EndpointHealth       = "/healthz"
)

// Config Stateless MCP HTTP sunucusunun bağımlılık ve çalışma parametrelerini taşır.
type Config struct {
	ServerName         string
	ServerVersion      string
	InstanceID         string
	JWTSigningSecret   []byte
	Store              store.StateStore
	TelemetryStepDelay time.Duration
	Logger             *slog.Logger
}

// Router hem AWS Lambda (httpadapter) hem de yerel/K8s HTTP sunucularında çalışan http.Handler implementasyonudur.
type Router struct {
	handler        http.Handler
	logger         *slog.Logger
	serverName     string
	serverVersion  string
	instanceID     string
	incidentTool   *tools.IncidentContextTool
	mitigationTool *tools.MitigationPlanTool
	telemetryTool  *tools.LiveTelemetryTool
}

// NewRouter tüm araçları ve SEP-2243 / JWT middleware zincirini birleştiren yeni bir Router oluşturur.
func NewRouter(cfg Config) (*Router, error) {
	if len(cfg.JWTSigningSecret) == 0 {
		return nil, middleware.ErrEmptySigningSecret
	}
	if cfg.Store == nil {
		return nil, store.ErrInvalidStoreInput
	}

	serverName := strings.TrimSpace(cfg.ServerName)
	if serverName == "" {
		serverName = DefaultServerName
	}
	serverVersion := strings.TrimSpace(cfg.ServerVersion)
	if serverVersion == "" {
		serverVersion = DefaultServerVersion
	}
	instanceID := strings.TrimSpace(cfg.InstanceID)
	if instanceID == "" {
		instanceID = "lambda-instance-default"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	r := &Router{
		logger:         logger,
		serverName:     serverName,
		serverVersion:  serverVersion,
		instanceID:     instanceID,
		incidentTool:   tools.NewIncidentContextTool(cfg.Store, instanceID),
		mitigationTool: tools.NewMitigationPlanTool(cfg.Store, instanceID),
		telemetryTool:  tools.NewLiveTelemetryTool(cfg.Store, instanceID, cfg.TelemetryStepDelay),
	}

	jwtMW := middleware.NewJWTAuthMiddleware(cfg.JWTSigningSecret)
	mcpPipeline := middleware.ValidateSEP2243Headers(jwtMW(http.HandlerFunc(r.handleMCP)))

	mux := http.NewServeMux()
	mux.HandleFunc(EndpointHealth, r.handleHealth)
	mux.Handle(EndpointMCP, mcpPipeline)

	r.handler = mux
	return r, nil
}

// ServeHTTP standart http.Handler arayüzünü uygular.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.handler.ServeHTTP(w, r)
}

func (rt *Router) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", middleware.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":          "ok",
		"stateless":       true,
		"protocolVersion": mcp.ProtocolVersion,
		"instanceId":      rt.instanceID,
	})
}

func (rt *Router) handleMCP(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, middleware.MaxRequestBodyBytes))
	if err != nil {
		rt.writeError(w, http.StatusBadRequest, nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeParseError,
			Message: "failed to read request body",
		})
		return
	}

	rpcReq, meta, rpcErr := mcp.ParseAndValidateRequest(bodyBytes)
	if rpcErr != nil {
		var reqID any
		if rpcReq != nil {
			reqID = rpcReq.ID
		}
		rt.writeError(w, http.StatusBadRequest, reqID, rpcErr)
		return
	}

	identity, _ := middleware.IdentityFromContext(r.Context())
	rt.logger.InfoContext(r.Context(), "handling stateless MCP request",
		slog.String("method", rpcReq.Method),
		slog.String("client", meta.ClientInfo.Name),
		slog.String("tenant_id", identity.TenantID),
		slog.String("instance_id", rt.instanceID),
	)

	switch rpcReq.Method {
	case mcp.MethodServerDiscover:
		discovery := mcp.DefaultServerDiscovery(rt.serverName, rt.serverVersion)
		rt.writeResult(w, rpcReq.ID, discovery)

	case mcp.MethodToolsList:
		listResult := mcp.ToolsListResult{
			Tools: []mcp.ToolDefinition{
				rt.incidentTool.Definition(),
				rt.mitigationTool.Definition(),
				rt.telemetryTool.Definition(),
			},
			TTLMs:      mcp.DefaultToolsListTTLMs,
			CacheScope: mcp.CacheScopeTenant,
		}
		rt.writeResult(w, rpcReq.ID, listResult)

	case mcp.MethodToolsCall:
		rt.handleToolsCall(w, r, rpcReq)

	default:
		rt.writeError(w, http.StatusNotFound, rpcReq.ID, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeMethodNotFound,
			Message: fmt.Sprintf("unsupported MCP method %q", rpcReq.Method),
		})
	}
}

func (rt *Router) handleToolsCall(w http.ResponseWriter, r *http.Request, rpcReq *mcp.JSONRPCRequest) {
	callParams, rpcErr := mcp.ParseToolCallParams(rpcReq.Params)
	if rpcErr != nil {
		rt.writeError(w, http.StatusBadRequest, rpcReq.ID, rpcErr)
		return
	}

	switch callParams.Name {
	case mcp.ToolIncidentContextManager:
		res, toolErr := rt.incidentTool.Execute(r.Context(), callParams.Arguments)
		if toolErr != nil {
			rt.writeError(w, httpStatusFromRPCCode(toolErr.Code), rpcReq.ID, toolErr)
			return
		}
		rt.writeResult(w, rpcReq.ID, res)

	case mcp.ToolExecuteMitigationPlan:
		res, toolErr := rt.mitigationTool.Execute(r.Context(), callParams.Arguments, callParams.ContinuationToken)
		if toolErr != nil {
			rt.writeError(w, httpStatusFromRPCCode(toolErr.Code), rpcReq.ID, toolErr)
			return
		}
		rt.writeResult(w, rpcReq.ID, res)

	case mcp.ToolLiveTelemetryDiagnostic:
		streamErr := rt.telemetryTool.StreamExecute(
			r.Context(),
			w,
			rpcReq.ID,
			callParams.Meta.ProgressToken,
			callParams.Arguments,
		)
		if streamErr != nil {
			rt.writeError(w, httpStatusFromRPCCode(streamErr.Code), rpcReq.ID, streamErr)
		}

	default:
		rt.writeError(w, http.StatusNotFound, rpcReq.ID, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeMethodNotFound,
			Message: fmt.Sprintf("unknown MCP tool %q", callParams.Name),
		})
	}
}

func (rt *Router) writeResult(w http.ResponseWriter, id any, result any) {
	w.Header().Set("Content-Type", middleware.ContentTypeJSON)
	w.WriteHeader(http.StatusOK)
	resp := mcp.NewJSONRPCResponse(id, result)
	_ = json.NewEncoder(w).Encode(resp)
}

func (rt *Router) writeError(w http.ResponseWriter, statusCode int, id any, rpcErr *mcp.JSONRPCError) {
	w.Header().Set("Content-Type", middleware.ContentTypeJSON)
	w.WriteHeader(statusCode)
	resp := mcp.NewJSONRPCErrorResponse(id, rpcErr.Code, rpcErr.Message, rpcErr.Data)
	_ = json.NewEncoder(w).Encode(resp)
}

func httpStatusFromRPCCode(code int) int {
	switch code {
	case middleware.ErrCodeUnauthorized:
		return http.StatusUnauthorized
	case mcp.ErrCodeInvalidParams, mcp.ErrCodeInvalidRequest, mcp.ErrCodeParseError,
		mcp.ErrCodeUnsupportedProtocolVersion, mcp.ErrCodeStatefulHandshakeForbidden, mcp.ErrCodeContinuationExpired:
		return http.StatusBadRequest
	case mcp.ErrCodeMethodNotFound:
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}
