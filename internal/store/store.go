package store

import (
	"context"
	"errors"
	"time"
)

// Varsayılan TTL süreleri ve durum sabitleri.
const (
	DefaultIncidentTTL     = 7 * 24 * time.Hour
	DefaultContinuationTTL = 15 * time.Minute

	IncidentStatusOpen     = "open"
	IncidentStatusResolved = "resolved"

	ContinuationStatusPending  = "pending"
	ContinuationStatusConsumed = "consumed"
)

// Depolama katmanı standart hata tanımları.
var (
	ErrIncidentNotFound          = errors.New("incident context not found")
	ErrIncidentAlreadyExists     = errors.New("incident context already exists")
	ErrContinuationNotFound      = errors.New("mitigation continuation token not found")
	ErrContinuationExpiredOrUsed = errors.New("mitigation continuation token is expired or already consumed")
	ErrInvalidStoreInput         = errors.New("invalid store input parameters")
)

// IncidentNote olay bağlamına bir mühendis tarafından eklenen zaman damgalı notu temsil eder.
type IncidentNote struct {
	AuthorEngineerID string    `json:"author_engineer_id" dynamodbav:"author_engineer_id"`
	Note             string    `json:"note" dynamodbav:"note"`
	CreatedAt        time.Time `json:"created_at" dynamodbav:"created_at"`
}

// IncidentContext kiracı bazlı izole edilen aktif olay durumunu temsil eder.
type IncidentContext struct {
	TenantID            string         `json:"tenant_id" dynamodbav:"tenant_id"`
	IncidentID          string         `json:"incident_id" dynamodbav:"incident_id"`
	Title               string         `json:"title" dynamodbav:"title"`
	Severity            string         `json:"severity" dynamodbav:"severity"`
	Status              string         `json:"status" dynamodbav:"status"`
	CreatedByEngineerID string         `json:"created_by_engineer_id" dynamodbav:"created_by_engineer_id"`
	Notes               []IncidentNote `json:"notes" dynamodbav:"notes"`
	CreatedAt           time.Time      `json:"created_at" dynamodbav:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at" dynamodbav:"updated_at"`
	ExpiresAtUnix       int64          `json:"expires_at_unix" dynamodbav:"ttl"`
}

// MitigationContinuation SEP-2322 çok turlu onay akışında DynamoDB'de saklanan bekleyen işlem durumudur.
type MitigationContinuation struct {
	ContinuationToken string    `json:"continuation_token" dynamodbav:"continuation_token"`
	TenantID          string    `json:"tenant_id" dynamodbav:"tenant_id"`
	EngineerID        string    `json:"engineer_id" dynamodbav:"engineer_id"`
	IncidentID        string    `json:"incident_id" dynamodbav:"incident_id"`
	Action            string    `json:"action" dynamodbav:"action"`
	TargetResource    string    `json:"target_resource" dynamodbav:"target_resource"`
	RiskLevel         string    `json:"risk_level" dynamodbav:"risk_level"`
	EstimatedCostUSD  float64   `json:"estimated_cost_usd" dynamodbav:"estimated_cost_usd"`
	Status            string    `json:"status" dynamodbav:"status"`
	CreatedAt         time.Time `json:"created_at" dynamodbav:"created_at"`
	ExpiresAt         time.Time `json:"expires_at" dynamodbav:"expires_at"`
	ExpiresAtUnix     int64     `json:"expires_at_unix" dynamodbav:"ttl"`
}

// StateStore stateless MCP sunucu kopyalarının uygulama durumunu paylaştığı soyut depolama arayüzüdür.
type StateStore interface {
	CreateIncident(ctx context.Context, incident IncidentContext) (*IncidentContext, error)
	AppendIncidentNote(ctx context.Context, tenantID, incidentID string, note IncidentNote) (*IncidentContext, error)
	GetIncident(ctx context.Context, tenantID, incidentID string) (*IncidentContext, error)
	SaveContinuation(ctx context.Context, cont MitigationContinuation) error
	ConsumeContinuation(ctx context.Context, tenantID, token string) (*MitigationContinuation, error)
}
