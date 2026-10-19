package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestInMemoryStore_IncidentAndContinuationLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	s := NewInMemoryStore()

	// Happy path: olay oluşturma, not ekleme ve okuma
	created, err := s.CreateIncident(ctx, IncidentContext{
		TenantID:            "tenant-a",
		IncidentID:          "INC-100",
		Title:               "RDS CPU Saturation",
		Severity:            "critical",
		CreatedByEngineerID: "eng-1",
	})
	if err != nil || created.Status != IncidentStatusOpen {
		t.Fatalf("expected open incident, got: %+v, err: %v", created, err)
	}

	// Edge case: aynı olay tekrar oluşturulursa ErrIncidentAlreadyExists dönmeli
	if _, err := s.CreateIncident(ctx, *created); !errors.Is(err, ErrIncidentAlreadyExists) {
		t.Fatalf("expected ErrIncidentAlreadyExists, got: %v", err)
	}

	updated, err := s.AppendIncidentNote(ctx, "tenant-a", "INC-100", IncidentNote{
		AuthorEngineerID: "eng-2",
		Note:             "Read replica lag exceeded 30s",
	})
	if err != nil || len(updated.Notes) != 1 {
		t.Fatalf("expected 1 note, got: %+v, err: %v", updated, err)
	}

	// Edge case: farklı kiracı aynı IncidentID'yi okuyamamalı (Tenant Isolation)
	if _, err := s.GetIncident(ctx, "tenant-b", "INC-100"); !errors.Is(err, ErrIncidentNotFound) {
		t.Fatalf("expected tenant isolation ErrIncidentNotFound, got: %v", err)
	}

	// Continuation tek seferlik tüketim (replay koruması)
	err = s.SaveContinuation(ctx, MitigationContinuation{
		ContinuationToken: "ct-1",
		TenantID:          "tenant-a",
		EngineerID:        "eng-1",
		IncidentID:        "INC-100",
		ExpiresAt:         time.Now().UTC().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("unexpected SaveContinuation error: %v", err)
	}
	if _, err := s.ConsumeContinuation(ctx, "tenant-a", "ct-1"); err != nil {
		t.Fatalf("expected first ConsumeContinuation to succeed, got: %v", err)
	}
	if _, err := s.ConsumeContinuation(ctx, "tenant-a", "ct-1"); !errors.Is(err, ErrContinuationExpiredOrUsed) {
		t.Fatalf("expected ErrContinuationExpiredOrUsed on second consume, got: %v", err)
	}
}

type mockDynamoClient struct {
	lastPut *dynamodb.PutItemInput
	items   map[string]map[string]types.AttributeValue
}

func (m *mockDynamoClient) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	m.lastPut = in
	pk := in.Item[pkAttr].(*types.AttributeValueMemberS).Value
	sk := in.Item[skAttr].(*types.AttributeValueMemberS).Value
	key := pk + "/" + sk
	if _, exists := m.items[key]; exists {
		return nil, &types.ConditionalCheckFailedException{}
	}
	m.items[key] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (m *mockDynamoClient) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	pk := in.Key[pkAttr].(*types.AttributeValueMemberS).Value
	sk := in.Key[skAttr].(*types.AttributeValueMemberS).Value
	return &dynamodb.GetItemOutput{Item: m.items[pk+"/"+sk]}, nil
}

func (m *mockDynamoClient) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	pk := in.Key[pkAttr].(*types.AttributeValueMemberS).Value
	sk := in.Key[skAttr].(*types.AttributeValueMemberS).Value
	item, ok := m.items[pk+"/"+sk]
	if !ok {
		return nil, &types.ConditionalCheckFailedException{}
	}
	var inc ddbIncidentItem
	_ = attributevalue.UnmarshalMap(item, &inc)
	inc.Notes = append(inc.Notes, IncidentNote{AuthorEngineerID: "eng-2", Note: "ddb note"})
	av, _ := attributevalue.MarshalMap(inc)
	m.items[pk+"/"+sk] = av
	return &dynamodb.UpdateItemOutput{Attributes: av}, nil
}

func TestDynamoDBStore_CreateAppendGet(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	mock := &mockDynamoClient{items: make(map[string]map[string]types.AttributeValue)}
	ddb, err := NewDynamoDBStore(mock, "mcp-incident-state")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = ddb.CreateIncident(ctx, IncidentContext{
		TenantID:   "tenant-ddb",
		IncidentID: "INC-200",
		Title:      "ALB 5xx Spike",
		Severity:   "high",
	})
	if err != nil {
		t.Fatalf("CreateIncident failed: %v", err)
	}

	if _, err := ddb.AppendIncidentNote(ctx, "tenant-ddb", "INC-200", IncidentNote{AuthorEngineerID: "eng-2", Note: "ddb note"}); err != nil {
		t.Fatalf("AppendIncidentNote failed: %v", err)
	}

	got, err := ddb.GetIncident(ctx, "tenant-ddb", "INC-200")
	if err != nil || got.Title != "ALB 5xx Spike" || len(got.Notes) != 1 {
		t.Fatalf("unexpected GetIncident result: %+v, err: %v", got, err)
	}
}
