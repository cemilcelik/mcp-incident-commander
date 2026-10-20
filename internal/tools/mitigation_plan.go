package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"mcp-incident-commander/internal/mcp"
	"mcp-incident-commander/internal/middleware"
	"mcp-incident-commander/internal/store"
)

// execute_mitigation_plan eşik değerleri, risk seviyeleri ve durum sabitleri.
const (
	CostApprovalThresholdUSD = 500.0
	continuationTokenBytes   = 16

	RiskLevelLow    = "low"
	RiskLevelMedium = "medium"
	RiskLevelHigh   = "high"

	MitigationStatusExecuted = "executed"
	MitigationStatusAborted  = "aborted"
)

// MitigationPlanArgs execute_mitigation_plan aracının ilk tur ve ikinci tur (onay) argümanlarını temsil eder.
type MitigationPlanArgs struct {
	IncidentID        string  `json:"incident_id,omitempty"`
	Action            string  `json:"action,omitempty"`
	TargetResource    string  `json:"target_resource,omitempty"`
	RiskLevel         string  `json:"risk_level,omitempty"`
	EstimatedCostUSD  float64 `json:"estimated_cost_usd,omitempty"`
	ContinuationToken string  `json:"continuation_token,omitempty"`
	Approved          *bool   `json:"approved,omitempty"`
	ApproverComment   string  `json:"approver_comment,omitempty"`
}

// MitigationExecutionResponse tamamlanan veya iptal edilen müdahale planının yapılandırılmış çıktısıdır.
type MitigationExecutionResponse struct {
	Status             string  `json:"status"`
	IncidentID         string  `json:"incident_id"`
	Action             string  `json:"action"`
	TargetResource     string  `json:"target_resource"`
	RiskLevel          string  `json:"risk_level"`
	EstimatedCostUSD   float64 `json:"estimated_cost_usd"`
	ApprovedRoundTrip  bool    `json:"approved_round_trip"`
	HandledByInstance  string  `json:"handled_by_instance"`
	ExecutedByEngineer string  `json:"executed_by_engineer"`
	Message            string  `json:"message"`
}

// MitigationPlanTool SEP-2322 Multi-Round-Trip onay akışını uygulayan kritik altyapı müdahale aracıdır.
type MitigationPlanTool struct {
	store      store.StateStore
	instanceID string
}

// NewMitigationPlanTool yeni bir MitigationPlanTool örneği oluşturur.
func NewMitigationPlanTool(stateStore store.StateStore, instanceID string) *MitigationPlanTool {
	if strings.TrimSpace(instanceID) == "" {
		instanceID = "lambda-instance-default"
	}
	return &MitigationPlanTool{
		store:      stateStore,
		instanceID: instanceID,
	}
}

// Definition tools/list yanıtı için execute_mitigation_plan JSON şemasını döner.
func (t *MitigationPlanTool) Definition() mcp.ToolDefinition {
	return mcp.ToolDefinition{
		Name:        mcp.ToolExecuteMitigationPlan,
		Description: "Executes infrastructure mitigation plans. Triggers SEP-2322 InputRequiredResult with continuation_token when estimated_cost_usd > 500 or risk_level == 'high'.",
		InputSchema: mcp.JSONSchema{
			Type: "object",
			Properties: map[string]mcp.JSONSchemaProperty{
				"incident_id": {
					Type:        "string",
					Description: "Target incident identifier (required on initial call)",
				},
				"action": {
					Type:        "string",
					Description: "Mitigation action to perform (e.g. scale_out_eks_nodegroup, failover_rds_multi_az)",
				},
				"target_resource": {
					Type:        "string",
					Description: "AWS resource identifier or ARN",
				},
				"risk_level": {
					Type:        "string",
					Description: "Operational risk level of the mitigation",
					Enum:        []string{RiskLevelLow, RiskLevelMedium, RiskLevelHigh},
				},
				"estimated_cost_usd": {
					Type:        "number",
					Description: "Estimated FinOps impact in USD",
				},
				"approved": {
					Type:        "boolean",
					Description: "Explicit confirmation flag for SEP-2322 second round-trip",
				},
				"approver_comment": {
					Type:        "string",
					Description: "Optional audit comment provided during approval",
				},
			},
		},
	}
}

// Execute ilk turda risk/maliyet analizi yapar, gerekirse SEP-2322 continuation_token üretir; ikinci turda onayı tamamlar.
func (t *MitigationPlanTool) Execute(ctx context.Context, rawArgs json.RawMessage, paramContinuationToken string) (*mcp.ToolCallResult, *mcp.JSONRPCError) {
	identity, ok := middleware.IdentityFromContext(ctx)
	if !ok {
		return nil, &mcp.JSONRPCError{
			Code:    middleware.ErrCodeUnauthorized,
			Message: "missing authenticated tenant_id and engineer_id in request context",
		}
	}

	var args MitigationPlanArgs
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, &mcp.JSONRPCError{
				Code:    mcp.ErrCodeInvalidParams,
				Message: "failed to parse execute_mitigation_plan arguments",
				Data:    err.Error(),
			}
		}
	}

	token := strings.TrimSpace(paramContinuationToken)
	if token == "" {
		token = strings.TrimSpace(args.ContinuationToken)
	}

	// 2. Tur (SEP-2322 Continuation Akışı): İstemci continuation_token ile onay yanıtını gönderdiğinde çalışır.
	if token != "" {
		return t.handleContinuationRoundTrip(ctx, identity, token, args)
	}

	// 1. Tur: İlk müdahale talebi doğrulaması.
	return t.handleInitialRoundTrip(ctx, identity, args)
}

func (t *MitigationPlanTool) handleInitialRoundTrip(ctx context.Context, identity middleware.Identity, args MitigationPlanArgs) (*mcp.ToolCallResult, *mcp.JSONRPCError) {
	args.IncidentID = strings.TrimSpace(args.IncidentID)
	args.Action = strings.TrimSpace(args.Action)
	args.TargetResource = strings.TrimSpace(args.TargetResource)
	args.RiskLevel = strings.ToLower(strings.TrimSpace(args.RiskLevel))

	if args.IncidentID == "" || args.Action == "" || args.TargetResource == "" {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "'incident_id', 'action', and 'target_resource' are required for initial mitigation call",
		}
	}

	if args.RiskLevel != RiskLevelLow && args.RiskLevel != RiskLevelMedium && args.RiskLevel != RiskLevelHigh {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: fmt.Sprintf("invalid risk_level %q: must be low, medium, or high", args.RiskLevel),
		}
	}

	if args.EstimatedCostUSD < 0 {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "estimated_cost_usd cannot be negative",
		}
	}

	requiresApproval := args.EstimatedCostUSD > CostApprovalThresholdUSD || args.RiskLevel == RiskLevelHigh
	if !requiresApproval {
		return t.buildExecutionResult(ctx, identity, args.IncidentID, args.Action, args.TargetResource, args.RiskLevel, args.EstimatedCostUSD, false, true, "")
	}

	// Bağlantıyı açık tutmadan durumu StateStore'a kaydet ve SEP-2322 InputRequiredResult dön.
	token, err := generateContinuationToken()
	if err != nil {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "failed to generate SEP-2322 continuation token",
		}
	}

	now := time.Now().UTC()
	expiresAt := now.Add(store.DefaultContinuationTTL)

	cont := store.MitigationContinuation{
		ContinuationToken: token,
		TenantID:          identity.TenantID,
		EngineerID:        identity.EngineerID,
		IncidentID:        args.IncidentID,
		Action:            args.Action,
		TargetResource:    args.TargetResource,
		RiskLevel:         args.RiskLevel,
		EstimatedCostUSD:  args.EstimatedCostUSD,
		CreatedAt:         now,
		ExpiresAt:         expiresAt,
	}

	if err := t.store.SaveContinuation(ctx, cont); err != nil {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "failed to persist mitigation continuation state",
			Data:    err.Error(),
		}
	}

	reason := fmt.Sprintf(
		"Mitigation action %q on %q requires explicit approval (estimated_cost_usd=%.2f, risk_level=%s)",
		args.Action, args.TargetResource, args.EstimatedCostUSD, args.RiskLevel,
	)

	res := mcp.NewInputRequiredToolResult(mcp.InputRequiredResult{
		ContinuationToken: token,
		Reason:            reason,
		RequiredFields: []mcp.RequiredInputField{
			{
				Name:        "approved",
				Type:        "boolean",
				Description: "Set to true to confirm execution or false to abort the mitigation plan",
				Required:    true,
			},
		},
		ExpiresAt: expiresAt.Format(time.RFC3339),
		Metadata: map[string]any{
			"incident_id":         args.IncidentID,
			"action":              args.Action,
			"target_resource":     args.TargetResource,
			"risk_level":          args.RiskLevel,
			"estimated_cost_usd":  args.EstimatedCostUSD,
			"handled_by_instance": t.instanceID,
		},
	})
	return &res, nil
}

func (t *MitigationPlanTool) handleContinuationRoundTrip(ctx context.Context, identity middleware.Identity, token string, args MitigationPlanArgs) (*mcp.ToolCallResult, *mcp.JSONRPCError) {
	if args.Approved == nil {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInvalidParams,
			Message: "missing required boolean 'approved' field when resuming with continuation_token",
		}
	}

	cont, err := t.store.ConsumeContinuation(ctx, identity.TenantID, token)
	if err != nil {
		if errors.Is(err, store.ErrContinuationNotFound) || errors.Is(err, store.ErrContinuationExpiredOrUsed) {
			return nil, &mcp.JSONRPCError{
				Code:    mcp.ErrCodeContinuationExpired,
				Message: err.Error(),
			}
		}
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "failed to consume mitigation continuation state",
			Data:    err.Error(),
		}
	}

	return t.buildExecutionResult(
		ctx,
		identity,
		cont.IncidentID,
		cont.Action,
		cont.TargetResource,
		cont.RiskLevel,
		cont.EstimatedCostUSD,
		true,
		*args.Approved,
		strings.TrimSpace(args.ApproverComment),
	)
}

func (t *MitigationPlanTool) buildExecutionResult(
	ctx context.Context,
	identity middleware.Identity,
	incidentID, action, targetResource, riskLevel string,
	estimatedCostUSD float64,
	approvedRoundTrip bool,
	approved bool,
	comment string,
) (*mcp.ToolCallResult, *mcp.JSONRPCError) {
	status := MitigationStatusExecuted
	msg := fmt.Sprintf("Mitigation %q executed on %q (cost=$%.2f, risk=%s)", action, targetResource, estimatedCostUSD, riskLevel)
	if !approved {
		status = MitigationStatusAborted
		msg = fmt.Sprintf("Mitigation %q on %q aborted by engineer %s", action, targetResource, identity.EngineerID)
	}
	if comment != "" {
		msg = fmt.Sprintf("%s [comment: %s]", msg, comment)
	}

	// Olay mevcutsa denetim (audit) izi olarak not eklenir (best-effort).
	_, _ = t.store.AppendIncidentNote(ctx, identity.TenantID, incidentID, store.IncidentNote{
		AuthorEngineerID: identity.EngineerID,
		Note:             msg,
	})

	payload, err := json.Marshal(MitigationExecutionResponse{
		Status:             status,
		IncidentID:         incidentID,
		Action:             action,
		TargetResource:     targetResource,
		RiskLevel:          riskLevel,
		EstimatedCostUSD:   estimatedCostUSD,
		ApprovedRoundTrip:  approvedRoundTrip,
		HandledByInstance:  t.instanceID,
		ExecutedByEngineer: identity.EngineerID,
		Message:            msg,
	})
	if err != nil {
		return nil, &mcp.JSONRPCError{
			Code:    mcp.ErrCodeInternalError,
			Message: "failed to serialize mitigation execution response",
		}
	}

	res := mcp.NewTextToolResult(string(payload), false)
	return &res, nil
}

func generateContinuationToken() (string, error) {
	buf := make([]byte, continuationTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "ct_" + hex.EncodeToString(buf), nil
}
