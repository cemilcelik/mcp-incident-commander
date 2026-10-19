package store

import (
	"context"
	"strings"
	"sync"
	"time"
)

// InMemoryStore yerel çalışma ve birim testler için thread-safe bellek içi StateStore implementasyonudur.
type InMemoryStore struct {
	mu            sync.RWMutex
	incidents     map[string]IncidentContext
	continuations map[string]MitigationContinuation
}

// NewInMemoryStore yeni ve boş bir InMemoryStore örneği döndürür.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		incidents:     make(map[string]IncidentContext),
		continuations: make(map[string]MitigationContinuation),
	}
}

func compositeKey(tenantID, resourceID string) string {
	return tenantID + "#" + resourceID
}

// cloneIncident paylaşılan dilim (slice) referanslarından kaynaklı veri yarışlarını (data race) önlemek için derin kopya üretir.
func cloneIncident(inc IncidentContext) IncidentContext {
	copied := inc
	if inc.Notes != nil {
		copied.Notes = make([]IncidentNote, len(inc.Notes))
		copy(copied.Notes, inc.Notes)
	} else {
		copied.Notes = []IncidentNote{}
	}
	return copied
}

// CreateIncident kiracı bazlı yeni bir olay bağlamı oluşturur; aynı ID mevcutsa ErrIncidentAlreadyExists döner.
func (s *InMemoryStore) CreateIncident(_ context.Context, incident IncidentContext) (*IncidentContext, error) {
	if strings.TrimSpace(incident.TenantID) == "" || strings.TrimSpace(incident.IncidentID) == "" {
		return nil, ErrInvalidStoreInput
	}

	now := time.Now().UTC()
	if incident.CreatedAt.IsZero() {
		incident.CreatedAt = now
	}
	incident.UpdatedAt = now
	if incident.Status == "" {
		incident.Status = IncidentStatusOpen
	}
	if incident.ExpiresAtUnix == 0 {
		incident.ExpiresAtUnix = now.Add(DefaultIncidentTTL).Unix()
	}
	if incident.Notes == nil {
		incident.Notes = []IncidentNote{}
	}

	key := compositeKey(incident.TenantID, incident.IncidentID)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.incidents[key]; exists {
		return nil, ErrIncidentAlreadyExists
	}

	stored := cloneIncident(incident)
	s.incidents[key] = stored
	out := cloneIncident(stored)
	return &out, nil
}

// AppendIncidentNote mevcut bir olaya atomik olarak yeni not ekler ve güncellenmiş durumu döner.
func (s *InMemoryStore) AppendIncidentNote(_ context.Context, tenantID, incidentID string, note IncidentNote) (*IncidentContext, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(incidentID) == "" || strings.TrimSpace(note.Note) == "" {
		return nil, ErrInvalidStoreInput
	}

	now := time.Now().UTC()
	if note.CreatedAt.IsZero() {
		note.CreatedAt = now
	}

	key := compositeKey(tenantID, incidentID)

	s.mu.Lock()
	defer s.mu.Unlock()

	inc, exists := s.incidents[key]
	if !exists {
		return nil, ErrIncidentNotFound
	}

	inc.Notes = append(inc.Notes, note)
	inc.UpdatedAt = now
	s.incidents[key] = inc

	out := cloneIncident(inc)
	return &out, nil
}

// GetIncident kiracı ve olay kimliğine göre aktif olay bağlamını okur.
func (s *InMemoryStore) GetIncident(_ context.Context, tenantID, incidentID string) (*IncidentContext, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(incidentID) == "" {
		return nil, ErrInvalidStoreInput
	}

	key := compositeKey(tenantID, incidentID)

	s.mu.RLock()
	defer s.mu.RUnlock()

	inc, exists := s.incidents[key]
	if !exists {
		return nil, ErrIncidentNotFound
	}

	out := cloneIncident(inc)
	return &out, nil
}

// SaveContinuation SEP-2322 onay akışı için devam jetonunu kaydeder.
func (s *InMemoryStore) SaveContinuation(_ context.Context, cont MitigationContinuation) error {
	if strings.TrimSpace(cont.TenantID) == "" || strings.TrimSpace(cont.ContinuationToken) == "" {
		return ErrInvalidStoreInput
	}

	now := time.Now().UTC()
	if cont.CreatedAt.IsZero() {
		cont.CreatedAt = now
	}
	if cont.ExpiresAt.IsZero() {
		cont.ExpiresAt = now.Add(DefaultContinuationTTL)
	}
	cont.ExpiresAtUnix = cont.ExpiresAt.Unix()
	cont.Status = ContinuationStatusPending

	key := compositeKey(cont.TenantID, cont.ContinuationToken)

	s.mu.Lock()
	defer s.mu.Unlock()

	s.continuations[key] = cont
	return nil
}

// ConsumeContinuation devam jetonunu doğrular, tek kullanımlık olarak tüketir (consumed) ve durumunu döner.
func (s *InMemoryStore) ConsumeContinuation(_ context.Context, tenantID, token string) (*MitigationContinuation, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(token) == "" {
		return nil, ErrInvalidStoreInput
	}

	key := compositeKey(tenantID, token)
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()

	cont, exists := s.continuations[key]
	if !exists {
		return nil, ErrContinuationNotFound
	}

	if cont.Status != ContinuationStatusPending || now.After(cont.ExpiresAt) {
		return nil, ErrContinuationExpiredOrUsed
	}

	cont.Status = ContinuationStatusConsumed
	s.continuations[key] = cont
	out := cont
	return &out, nil
}
