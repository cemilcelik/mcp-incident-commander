package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/store"
)

// incident_context_manager eylem ve önem derecesi sabitleri.
const (
	ActionCreateIncident = "create"
	ActionAppendNote     = "append_note"
	ActionGetIncident    = "get"

	SeverityLow      = "low"
	SeverityMedium   = "medium"
	SeverityHigh     = "high"
	SeverityCritical = "critical"
)

// IncidentContextArgs incident_context_manager aracına gönderilen JSON argümanlarını temsil eder.
type IncidentContextArgs struct {
	Action     string `json:"action"`
	IncidentID string `json:"incident_id"`
	Title      string `json:"title,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Note       string `json:"note,omitempty"`
}

// IncidentContextResponse araç çıktısında olay durumuyla birlikte hangi stateless sunucu kopyasının yanıt verdiğini döner.
type IncidentContextResponse struct {
	Action             string                `json:"action"`
	HandledByInstance  string                `json:"handled_by_instance"`
	ExecutedByEngineer string                `json:"executed_by_engineer"`
	Incident           *store.IncidentContext `json:"incident"`
}

// IncidentContextTool protokol seviyesinde stateless çalışırken uygulama durumunu StateStore üzerinde yöneten araçtır.
type IncidentContextTool struct {
	store      store.StateStore
	instanceID string
}

// NewIncidentContextTool yeni bir IncidentContextTool örneği oluşturur.
func NewIncidentContextTool(stateStore store.StateStore, instanceID string) *IncidentContextTool {
	if strings.TrimSpace(instanceID) == "" {
		instanceID = "lambda-instance-default"
	}
	return &IncidentContextTool{
		store:      stateStore,
		instanceID: instanceID,
	}
}

// Definition tools/list yanıtı için incident_context_manager JSON şemasını döner.
func (t *IncidentContextTool) Definition() mcp.ToolDefinition {
	return mcp.ToolDefinition{
		Name:        mcp.ToolIncidentContextManager,
		Description: "Creates, appends diagnostic notes to, and retrieves tenant-isolated incident context from DynamoDB across stateless instances.",
		InputSchema: mcp.JSONSchema{
			Type: "object",
			Properties: map[string]mcp.JSONSchemaProperty{
				"action": {
					Type:        "string",
					Description: "Operation to perform on the incident context",
					Enum:        []string{ActionCreateIncident, ActionAppendNote, ActionGetIncident},
				},
				"incident_id": {
					Type:        "string",
					Description: "Unique identifier of the incident (e.g. INC-2026-001)",
				},
				"title": {
					Type:        "string",
					Description: "Incident summary title (required when action is 'create')",
				},
				"severity": {
					Type:        "string",
					Description: "Incident severity level",
					Enum:        []string{SeverityLow, SeverityMedium, SeverityHigh, SeverityCritical},
				},
				"note": {
					Type:        "string",
					Description: "Diagnostic or mitigation note to append (required when action is 'append_note')",
				},
			},
			Required: []string{"action", "incident_id"},
		},
	}
}

// Execute istemcinin JWT kimliğindeki TenantID ve EngineerID bağlamıyla olay işlemini çalıştırır.
func (t *IncidentContextTool) Execute(ctx context.Context, rawArgs json.RawMessage) (*mcp.ToolCallResult, *mcp.JSONRPCError) {
	identity, ok := middleware.IdentityFromContext(ctx)
	if !ok {
		return nil, &mcp.JSONRPCError{
			Code:    middleware.ErrCodeUnauthorized,
			Message: "missing authenticated tenant_id and engineer_id in request context",
		}
	}

	var args IncidentContextArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "failed to parse incident_context_manager arguments",
			Data:    err.Error(),
		}
	}

	args.IncidentID = strings.TrimSpace(args.IncidentID)
	args.Action = strings.TrimSpace(args.Action)
	if args.IncidentID == "" || args.Action == "" {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "both 'action' and 'incident_id' are required",
		}
	}

	var (
		incident *store.IncidentContext
		err      error
	)

	switch args.Action {
	case ActionCreateIncident:
		title := strings.TrimSpace(args.Title)
		if title == "" {
			return nil, &mcp.JSONRPCError{
				Code:    mcp.ErrCodeInvalidParams,
				Message: "'title' is required when action is 'create'",
			}
		}
		severity := strings.TrimSpace(args.Severity)
		if severity == "" {
			severity = SeverityMedium
		}

		var initialNotes []store.IncidentNote
		if noteText := strings.TrimSpace(args.Note); noteText != "" {
			initialNotes = []store.IncidentNote{
				{
					AuthorEngineerID: identity.EngineerID,
					Note:             noteText,
				},
			}
		}

		incident, err = t.store.CreateIncident(ctx, store.IncidentContext{
			TenantID:            identity.TenantID,
			IncidentID:          args.IncidentID,
			Title:               title,
			Severity:            severity,
			Status:              store.IncidentStatusOpen,
			CreatedByEngineerID: identity.EngineerID,
			Notes:               initialNotes,
		})

	case ActionAppendNote:
		noteText := strings.TrimSpace(args.Note)
		if noteText == "" {
			return nil, &mcp.JSONRPCError{
				Code:    mcp.ErrCodeInvalidParams,
				Message: "'note' is required when action is 'append_note'",
			}
		}
		incident, err = t.store.AppendIncidentNote(ctx, identity.TenantID, args.IncidentID, store.IncidentNote{
			AuthorEngineerID: identity.EngineerID,
			Note:             noteText,
		})

	case ActionGetIncident:
		incident, err = t.store.GetIncident(ctx, identity.TenantID, args.IncidentID)

	default:
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: fmt.Sprintf("unsupported action %q: must be create, append_note, or get", args.Action),
		}
	}

	if err != nil {
		if errors.Is(err, store.ErrIncidentNotFound) || errors.Is(err, store.ErrIncidentAlreadyExists) {
			res := mcp.NewTextToolResult(err.Error(), true)
			return &res, nil
		}
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "failed to access incident state store",
			Data:    err.Error(),
		}
	}

	payload, err := json.Marshal(IncidentContextResponse{
		Action:             args.Action,
		HandledByInstance:  t.instanceID,
		ExecutedByEngineer: identity.EngineerID,
		Incident:           incident,
	})
	if err != nil {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "failed to serialize incident context response",
		}
	}

	res := mcp.NewTextToolResult(string(payload), false)
	return &res, nil
}
