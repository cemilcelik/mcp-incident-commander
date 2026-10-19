package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DynamoDB Single-Table Design anahtar ve önek sabitleri.
const (
	pkAttr             = "pk"
	skAttr             = "sk"
	tenantPrefix       = "TENANT#"
	incidentPrefix     = "INCIDENT#"
	continuationPrefix = "CONTINUATION#"
)

// DynamoDBAPI AWS SDK v2 DynamoDB istemcisini soyutlayarak test edilebilirliği sağlar.
type DynamoDBAPI interface {
	PutItem(ctx context.Context, params *dynamodb.PutItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	GetItem(ctx context.Context, params *dynamodb.GetItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	UpdateItem(ctx context.Context, params *dynamodb.UpdateItemInput, optFns ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
}

// DynamoDBStore AWS DynamoDB üzerinde çalışan StateStore implementasyonudur.
type DynamoDBStore struct {
	client    DynamoDBAPI
	tableName string
}

// NewDynamoDBStore verilen DynamoDB istemcisi ve tablo adıyla yeni bir DynamoDBStore oluşturur.
func NewDynamoDBStore(client DynamoDBAPI, tableName string) (*DynamoDBStore, error) {
	if client == nil || strings.TrimSpace(tableName) == "" {
		return nil, ErrInvalidStoreInput
	}
	return &DynamoDBStore{
		client:    client,
		tableName: strings.TrimSpace(tableName),
	}, nil
}

type ddbIncidentItem struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
	IncidentContext
}

type ddbContinuationItem struct {
	PK string `dynamodbav:"pk"`
	SK string `dynamodbav:"sk"`
	MitigationContinuation
}

func tenantPK(tenantID string) string {
	return tenantPrefix + tenantID
}

func incidentSK(incidentID string) string {
	return incidentPrefix + incidentID
}

func continuationSK(token string) string {
	return continuationPrefix + token
}

func buildPrimaryKey(pk, sk string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		pkAttr: &types.AttributeValueMemberS{Value: pk},
		skAttr: &types.AttributeValueMemberS{Value: sk},
	}
}

// CreateIncident DynamoDB'de koşullu yazma (attribute_not_exists) ile yeni bir olay bağlamı oluşturur.
func (d *DynamoDBStore) CreateIncident(ctx context.Context, incident IncidentContext) (*IncidentContext, error) {
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

	item := ddbIncidentItem{
		PK:              tenantPK(incident.TenantID),
		SK:              incidentSK(incident.IncidentID),
		IncidentContext: incident,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal incident item: %w", err)
	}

	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(d.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk) AND attribute_not_exists(sk)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return nil, ErrIncidentAlreadyExists
		}
		return nil, fmt.Errorf("dynamodb PutItem failed for incident: %w", err)
	}

	return &incident, nil
}

// AppendIncidentNote DynamoDB list_append ifadesiyle yarış durumuna (race condition) yol açmadan olaya not ekler.
func (d *DynamoDBStore) AppendIncidentNote(ctx context.Context, tenantID, incidentID string, note IncidentNote) (*IncidentContext, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(incidentID) == "" || strings.TrimSpace(note.Note) == "" {
		return nil, ErrInvalidStoreInput
	}

	now := time.Now().UTC()
	if note.CreatedAt.IsZero() {
		note.CreatedAt = now
	}

	noteListAV, err := attributevalue.MarshalList([]IncidentNote{note})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal incident note: %w", err)
	}
	nowAV, err := attributevalue.Marshal(now)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal timestamp: %w", err)
	}

	out, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(d.tableName),
		Key:                 buildPrimaryKey(tenantPK(tenantID), incidentSK(incidentID)),
		ConditionExpression: aws.String("attribute_exists(pk) AND attribute_exists(sk)"),
		UpdateExpression:    aws.String("SET notes = list_append(if_not_exists(notes, :empty_list), :new_note), updated_at = :now"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":new_note":   &types.AttributeValueMemberL{Value: noteListAV},
			":empty_list": &types.AttributeValueMemberL{Value: []types.AttributeValue{}},
			":now":        nowAV,
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return nil, ErrIncidentNotFound
		}
		return nil, fmt.Errorf("dynamodb UpdateItem failed for incident note: %w", err)
	}

	var updated ddbIncidentItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &updated); err != nil {
		return nil, fmt.Errorf("failed to unmarshal updated incident: %w", err)
	}
	return &updated.IncidentContext, nil
}

// GetIncident kiracı ve olay kimliğine göre DynamoDB'den olay bağlamını tutarlı okuma (ConsistentRead) ile getirir.
func (d *DynamoDBStore) GetIncident(ctx context.Context, tenantID, incidentID string) (*IncidentContext, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(incidentID) == "" {
		return nil, ErrInvalidStoreInput
	}

	out, err := d.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName:      aws.String(d.tableName),
		Key:            buildPrimaryKey(tenantPK(tenantID), incidentSK(incidentID)),
		ConsistentRead: aws.Bool(true),
	})
	if err != nil {
		return nil, fmt.Errorf("dynamodb GetItem failed for incident: %w", err)
	}
	if len(out.Item) == 0 {
		return nil, ErrIncidentNotFound
	}

	var item ddbIncidentItem
	if err := attributevalue.UnmarshalMap(out.Item, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal incident item: %w", err)
	}
	return &item.IncidentContext, nil
}

// SaveContinuation SEP-2322 onay akışı için devam jetonunu TTL ile birlikte DynamoDB'ye kaydeder.
func (d *DynamoDBStore) SaveContinuation(ctx context.Context, cont MitigationContinuation) error {
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

	item := ddbContinuationItem{
		PK:                     tenantPK(cont.TenantID),
		SK:                     continuationSK(cont.ContinuationToken),
		MitigationContinuation: cont,
	}

	av, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("failed to marshal continuation item: %w", err)
	}

	_, err = d.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(d.tableName),
		Item:      av,
	})
	if err != nil {
		return fmt.Errorf("dynamodb PutItem failed for continuation: %w", err)
	}
	return nil
}

// ConsumeContinuation devam jetonunu koşullu güncelleme ile tek seferlik tüketir (replay saldırılarını önler).
func (d *DynamoDBStore) ConsumeContinuation(ctx context.Context, tenantID, token string) (*MitigationContinuation, error) {
	if strings.TrimSpace(tenantID) == "" || strings.TrimSpace(token) == "" {
		return nil, ErrInvalidStoreInput
	}

	nowUnix := time.Now().UTC().Unix()
	nowAV, err := attributevalue.Marshal(nowUnix)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal current unix time: %w", err)
	}

	out, err := d.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           aws.String(d.tableName),
		Key:                 buildPrimaryKey(tenantPK(tenantID), continuationSK(token)),
		ConditionExpression: aws.String("attribute_exists(pk) AND #st = :pending AND #ttl > :now_unix"),
		UpdateExpression:    aws.String("SET #st = :consumed"),
		ExpressionAttributeNames: map[string]string{
			"#st":  "status",
			"#ttl": "ttl",
		},
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pending":  &types.AttributeValueMemberS{Value: ContinuationStatusPending},
			":consumed": &types.AttributeValueMemberS{Value: ContinuationStatusConsumed},
			":now_unix": nowAV,
		},
		ReturnValues: types.ReturnValueAllNew,
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return nil, ErrContinuationExpiredOrUsed
		}
		return nil, fmt.Errorf("dynamodb UpdateItem failed for continuation consumption: %w", err)
	}

	var item ddbContinuationItem
	if err := attributevalue.UnmarshalMap(out.Attributes, &item); err != nil {
		return nil, fmt.Errorf("failed to unmarshal consumed continuation: %w", err)
	}
	return &item.MitigationContinuation, nil
}
