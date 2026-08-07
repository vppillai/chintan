package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

// DynamoStore implements the Store interface using DynamoDB with a single table design.
// Single-table keys: PK=USER#<userID>, SK=SETTINGS | NOTE#<id> | CAPTURE#<id>
type DynamoStore struct {
	client    *dynamodb.Client
	tableName string
}

// NewDynamoStore creates a new DynamoDB-backed store.
func NewDynamoStore(client *dynamodb.Client, tableName string) *DynamoStore {
	return &DynamoStore{
		client:    client,
		tableName: tableName,
	}
}

// Key mapping helpers for single-table design
func userPK(userID string) string {
	return fmt.Sprintf("USER#%s", userID)
}

func settingsSK() string {
	return "SETTINGS"
}

func noteSK(noteID string) string {
	return fmt.Sprintf("NOTE#%s", noteID)
}

func captureSK(captureID string) string {
	return fmt.Sprintf("CAPTURE#%s", captureID)
}

// dynamoItem represents the structure stored in DynamoDB
type dynamoItem struct {
	PK   string `dynamodb:"pk"`
	SK   string `dynamodb:"sk"`
	Type string `dynamodb:"type"`
	Data string `dynamodb:"data"` // JSON-encoded model data
}

func (s *DynamoStore) GetSettings(ctx context.Context, userID string) (model.Settings, error) {
	if err := ctx.Err(); err != nil {
		return model.Settings{}, err
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
			"sk": &types.AttributeValueMemberS{Value: settingsSK()},
		},
	})
	if err != nil {
		return model.Settings{}, fmt.Errorf("dynamo get settings: %w", err)
	}

	if result.Item == nil {
		return defaultSettings(), nil
	}

	var item dynamoItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return model.Settings{}, fmt.Errorf("dynamo unmarshal settings: %w", err)
	}

	var settings model.Settings
	if err := json.Unmarshal([]byte(item.Data), &settings); err != nil {
		return model.Settings{}, fmt.Errorf("dynamo decode settings: %w", err)
	}

	return settings, nil
}

func (s *DynamoStore) PutSettings(ctx context.Context, userID string, settings model.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("dynamo encode settings: %w", err)
	}

	item := dynamoItem{
		PK:   userPK(userID),
		SK:   settingsSK(),
		Type: "settings",
		Data: string(data),
	}

	itemMap, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("dynamo marshal settings: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      itemMap,
	})
	if err != nil {
		return fmt.Errorf("dynamo put settings: %w", err)
	}

	return nil
}

func (s *DynamoStore) ListNotes(ctx context.Context, userID string) ([]model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        &types.AttributeValueMemberS{Value: userPK(userID)},
			":sk_prefix": &types.AttributeValueMemberS{Value: "NOTE#"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("dynamo query notes: %w", err)
	}

	notes := make([]model.NoteIndex, 0, len(result.Items))
	for _, item := range result.Items {
		var dynamoItem dynamoItem
		if err := attributevalue.UnmarshalMap(item, &dynamoItem); err != nil {
			return nil, fmt.Errorf("dynamo unmarshal note item: %w", err)
		}

		var note model.NoteIndex
		if err := json.Unmarshal([]byte(dynamoItem.Data), &note); err != nil {
			return nil, fmt.Errorf("dynamo decode note: %w", err)
		}

		notes = append(notes, note)
	}

	return notes, nil
}

func (s *DynamoStore) GetNote(ctx context.Context, userID, noteID string) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
			"sk": &types.AttributeValueMemberS{Value: noteSK(noteID)},
		},
	})
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("dynamo get note: %w", err)
	}

	if result.Item == nil {
		return model.NoteIndex{}, ErrNotFound
	}

	var item dynamoItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return model.NoteIndex{}, fmt.Errorf("dynamo unmarshal note: %w", err)
	}

	var note model.NoteIndex
	if err := json.Unmarshal([]byte(item.Data), &note); err != nil {
		return model.NoteIndex{}, fmt.Errorf("dynamo decode note: %w", err)
	}

	return note, nil
}

func (s *DynamoStore) PutNote(ctx context.Context, userID string, note model.NoteIndex) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("dynamo encode note: %w", err)
	}

	item := dynamoItem{
		PK:   userPK(userID),
		SK:   noteSK(note.ID),
		Type: "note",
		Data: string(data),
	}

	itemMap, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("dynamo marshal note: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      itemMap,
	})
	if err != nil {
		return fmt.Errorf("dynamo put note: %w", err)
	}

	return nil
}

func (s *DynamoStore) DeleteNote(ctx context.Context, userID, noteID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
			"sk": &types.AttributeValueMemberS{Value: noteSK(noteID)},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo delete note: %w", err)
	}

	return nil
}

func (s *DynamoStore) PutCapture(ctx context.Context, capture model.CaptureIndex) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	data, err := json.Marshal(capture)
	if err != nil {
		return fmt.Errorf("dynamo encode capture: %w", err)
	}

	item := dynamoItem{
		PK:   userPK(capture.UserID),
		SK:   captureSK(capture.ID),
		Type: "capture",
		Data: string(data),
	}

	itemMap, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("dynamo marshal capture: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      itemMap,
	})
	if err != nil {
		return fmt.Errorf("dynamo put capture: %w", err)
	}

	return nil
}

func (s *DynamoStore) GetCapture(ctx context.Context, userID, captureID string) (model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.CaptureIndex{}, err
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
			"sk": &types.AttributeValueMemberS{Value: captureSK(captureID)},
		},
	})
	if err != nil {
		return model.CaptureIndex{}, fmt.Errorf("dynamo get capture: %w", err)
	}

	if result.Item == nil {
		return model.CaptureIndex{}, ErrNotFound
	}

	var item dynamoItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return model.CaptureIndex{}, fmt.Errorf("dynamo unmarshal capture: %w", err)
	}

	var capture model.CaptureIndex
	if err := json.Unmarshal([]byte(item.Data), &capture); err != nil {
		return model.CaptureIndex{}, fmt.Errorf("dynamo decode capture: %w", err)
	}

	return capture, nil
}

func (s *DynamoStore) ListCapturesByNote(ctx context.Context, userID, noteID string) ([]model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        &types.AttributeValueMemberS{Value: userPK(userID)},
			":sk_prefix": &types.AttributeValueMemberS{Value: "CAPTURE#"},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("dynamo query captures: %w", err)
	}

	captures := make([]model.CaptureIndex, 0, len(result.Items))
	for _, item := range result.Items {
		var dynamoItem dynamoItem
		if err := attributevalue.UnmarshalMap(item, &dynamoItem); err != nil {
			return nil, fmt.Errorf("dynamo unmarshal capture item: %w", err)
		}
		var capture model.CaptureIndex
		if err := json.Unmarshal([]byte(dynamoItem.Data), &capture); err != nil {
			return nil, fmt.Errorf("dynamo decode capture: %w", err)
		}
		if capture.NoteID == noteID {
			captures = append(captures, capture)
		}
	}

	sort.Slice(captures, func(i, j int) bool {
		return captures[i].CreatedAt > captures[j].CreatedAt
	})
	return captures, nil
}

func (s *DynamoStore) UpdateCaptureStatus(ctx context.Context, userID, captureID string, status model.CaptureStatus, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// First get the current capture to preserve other fields
	capture, err := s.GetCapture(ctx, userID, captureID)
	if err != nil {
		return err
	}

	// Update the status and error fields
	capture.Status = status
	capture.Error = errMsg

	// Save the updated capture
	return s.PutCapture(ctx, capture)
}
