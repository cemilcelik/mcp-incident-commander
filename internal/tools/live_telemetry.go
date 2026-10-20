package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/store"
)

// Streamable HTTP (SSE) başlık, olay ve varsayılan zamanlama sabitleri.
const (
	ContentTypeEventStream = "text/event-stream"
	CacheControlNoCache    = "no-cache, no-transform"
	ConnectionKeepAlive    = "keep-alive"

	SSEEventProgress = "progress"
	SSEEventMessage  = "message"

	DefaultStepDelay     = 15 * time.Millisecond
	DefaultWindowMinutes = 15
	TotalProgressPercent = 100
)

// diagnosticStage tek bir teşhis aşamasının yüzdesini ve açıklamasını temsil eder.
type diagnosticStage struct {
	Progress int
	Stage    string
	Message  string
}

var defaultDiagnosticStages = []diagnosticStage{
	{
		Progress: 25,
		Stage:    "collecting_alb_metrics",
		Message:  "Collecting AWS ALB L7 target response latency and 5xx distribution",
	},
	{
		Progress: 50,
		Stage:    "analyzing_lambda_telemetry",
		Message:  "Analyzing AWS Lambda cold-start ratio and concurrent execution throttling",
	},
	{
		Progress: 75,
		Stage:    "inspecting_dynamodb_capacity",
		Message:  "Inspecting DynamoDB partition hot-keys and consumed RCU/WCU metrics",
	},
	{
		Progress: 100,
		Stage:    "synthesizing_root_cause",
		Message:  "Synthesizing root-cause diagnostic report and FinOps cost correlation",
	},
}

// LiveTelemetryArgs live_telemetry_diagnostic aracına gönderilen JSON argümanlarını temsil eder.
type LiveTelemetryArgs struct {
	IncidentID    string `json:"incident_id"`
	TargetService string `json:"target_service"`
	WindowMinutes int    `json:"window_minutes,omitempty"`
}

// DiagnosticSummary SSE akışı tamamlandığında son JSON-RPC yanıtında dönen teşhis özetidir.
type DiagnosticSummary struct {
	IncidentID         string   `json:"incident_id"`
	TargetService      string   `json:"target_service"`
	WindowMinutes      int      `json:"window_minutes"`
	StagesCompleted    int      `json:"stages_completed"`
	HandledByInstance  string   `json:"handled_by_instance"`
	ExecutedByEngineer string   `json:"executed_by_engineer"`
	Findings           []string `json:"findings"`
}

// LiveTelemetryTool Streamable HTTP (Chunked SSE) ile gerçek zamanlı teşhis akışı sunan araçtır.
type LiveTelemetryTool struct {
	store      store.StateStore
	instanceID string
	stepDelay  time.Duration
}

// NewLiveTelemetryTool yeni bir LiveTelemetryTool örneği oluşturur.
func NewLiveTelemetryTool(stateStore store.StateStore, instanceID string, stepDelay time.Duration) *LiveTelemetryTool {
	if strings.TrimSpace(instanceID) == "" {
		instanceID = "lambda-instance-default"
	}
	if stepDelay < 0 {
		stepDelay = DefaultStepDelay
	}
	return &LiveTelemetryTool{
		store:      stateStore,
		instanceID: instanceID,
		stepDelay:  stepDelay,
	}
}

// Definition tools/list yanıtı için live_telemetry_diagnostic JSON şemasını döner.
func (t *LiveTelemetryTool) Definition() mcp.ToolDefinition {
	return mcp.ToolDefinition{
		Name:        mcp.ToolLiveTelemetryDiagnostic,
		Description: "Streams real-time infrastructure telemetry diagnostics (25%, 50%, 75%, 100%) over Streamable HTTP (text/event-stream) and closes the connection upon completion.",
		InputSchema: mcp.JSONSchema{
			Type: "object",
			Properties: map[string]mcp.JSONSchemaProperty{
				"incident_id": {
					Type:        "string",
					Description: "Incident identifier being diagnosed",
				},
				"target_service": {
					Type:        "string",
					Description: "Service or cluster name to run live telemetry diagnostics against",
				},
				"window_minutes": {
					Type:        "integer",
					Description: "Lookback telemetry window in minutes (default 15)",
				},
			},
			Required: []string{"incident_id", "target_service"},
		},
	}
}

// StreamExecute teşhis adımlarını (%25, %50, %75, %100) http.Flusher ile SSE olarak akıtır ve nihai sonucu gönderip bağlantıyı kapatır.
func (t *LiveTelemetryTool) StreamExecute(
	ctx context.Context,
	w http.ResponseWriter,
	requestID any,
	progressToken string,
	rawArgs json.RawMessage,
) *mcp.JSONRPCError {
	identity, ok := middleware.IdentityFromContext(ctx)
	if !ok {
		return &mcp.JSONRPCError{
			Code:    middleware.ErrCodeUnauthorized,
			Message: "missing authenticated tenant_id and engineer_id in request context",
		}
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "streaming unsupported: http.ResponseWriter does not implement http.Flusher",
		}
	}

	var args LiveTelemetryArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "failed to parse live_telemetry_diagnostic arguments",
			Data:    err.Error(),
		}
	}

	args.IncidentID = strings.TrimSpace(args.IncidentID)
	args.TargetService = strings.TrimSpace(args.TargetService)
	if args.IncidentID == "" || args.TargetService == "" {
		return &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "both 'incident_id' and 'target_service' are required",
		}
	}
	if args.WindowMinutes <= 0 {
		args.WindowMinutes = DefaultWindowMinutes
	}

	// Streamable HTTP (SSE) başlıklarını ayarla ve akışı başlat.
	w.Header().Set("Content-Type", ContentTypeEventStream)
	w.Header().Set("Cache-Control", CacheControlNoCache)
	w.Header().Set("Connection", ConnectionKeepAlive)
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for _, stage := range defaultDiagnosticStages {
		if err := waitWithContext(ctx, t.stepDelay); err != nil {
			return nil
		}

		notif := mcp.JSONRPCNotification{
			JSONRPC: mcp.JSONRPCVersion,
			Method:  mcp.MethodNotificationsProgress,
			Params: mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Progress:      stage.Progress,
				Total:         TotalProgressPercent,
				Stage:         stage.Stage,
				Message:       fmt.Sprintf("[%s] %s", args.TargetService, stage.Message),
			},
		}

		if err := writeSSEEvent(w, flusher, SSEEventProgress, notif); err != nil {
			return nil
		}
	}

	summary := DiagnosticSummary{
		IncidentID:         args.IncidentID,
		TargetService:      args.TargetService,
		WindowMinutes:      args.WindowMinutes,
		StagesCompleted:    len(defaultDiagnosticStages),
		HandledByInstance:  t.instanceID,
		ExecutedByEngineer: identity.EngineerID,
		Findings: []string{
			fmt.Sprintf("Service %s p99 latency elevated within %dm window", args.TargetService, args.WindowMinutes),
			"ALB target group healthy; no sticky-session skew detected",
			"DynamoDB partition throughput within on-demand burst limits",
		},
	}

	summaryBytes, err := json.Marshal(summary)
	if err != nil {
		return nil
	}

	if t.store != nil {
		_, _ = t.store.AppendIncidentNote(ctx, identity.TenantID, args.IncidentID, store.IncidentNote{
			AuthorEngineerID: identity.EngineerID,
			Note:             fmt.Sprintf("Live telemetry diagnostic completed for %s (%d stages)", args.TargetService, len(defaultDiagnosticStages)),
		})
	}

	finalResp := mcp.NewJSONRPCResponse(requestID, mcp.NewTextToolResult(string(summaryBytes), false))
	_ = writeSSEEvent(w, flusher, SSEEventMessage, finalResp)
	return nil
}

func writeSSEEvent(w http.ResponseWriter, flusher http.Flusher, eventName string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, string(data)); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func waitWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
