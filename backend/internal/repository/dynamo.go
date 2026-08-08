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

func webAuthnChallengePK(challengeID string) string {
	return "WACHAL#" + challengeID
}

func webAuthnCredSK(credentialID string) string {
	return "WACRED#" + credentialID
}

func refreshVaultSK() string {
	return "WAREFRESH"
}

const pkWebAuthnCredList = "WACREDLIST"

// dynamoItem represents the structure stored in DynamoDB
type dynamoItem struct {
	PK   string `dynamodbav:"pk"`
	SK   string `dynamodbav:"sk"`
	Type string `dynamodbav:"type"`
	Data string `dynamodbav:"data"` // JSON-encoded model data
	TTL  int64  `dynamodbav:"ttl,omitempty"`
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

func (s *DynamoStore) DeleteCapture(ctx context.Context, userID, captureID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
			"sk": &types.AttributeValueMemberS{Value: captureSK(captureID)},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		var condErr *types.ConditionalCheckFailedException
		if errors.As(err, &condErr) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo delete capture: %w", err)
	}

	return nil
}

func (s *DynamoStore) putJSONItem(ctx context.Context, pk, sk, typ, data string, ttl int64) error {
	item := dynamoItem{PK: pk, SK: sk, Type: typ, Data: data, TTL: ttl}
	itemMap, err := attributevalue.MarshalMap(item)
	if err != nil {
		return fmt.Errorf("dynamo marshal: %w", err)
	}
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      itemMap,
	})
	if err != nil {
		return fmt.Errorf("dynamo put: %w", err)
	}
	return nil
}

func (s *DynamoStore) getJSONItem(ctx context.Context, pk, sk string) (dynamoItem, error) {
	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
			"sk": &types.AttributeValueMemberS{Value: sk},
		},
	})
	if err != nil {
		return dynamoItem{}, err
	}
	if result.Item == nil {
		return dynamoItem{}, ErrNotFound
	}
	var item dynamoItem
	if err := attributevalue.UnmarshalMap(result.Item, &item); err != nil {
		return dynamoItem{}, err
	}
	return item, nil
}

func (s *DynamoStore) PutWebAuthnChallenge(ctx context.Context, c model.WebAuthnChallenge) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	pk := webAuthnChallengePK(c.ChallengeID)
	return s.putJSONItem(ctx, pk, pk, "wachal", string(data), c.ExpiresAt)
}

func (s *DynamoStore) GetWebAuthnChallenge(ctx context.Context, challengeID string) (model.WebAuthnChallenge, error) {
	if err := ctx.Err(); err != nil {
		return model.WebAuthnChallenge{}, err
	}
	pk := webAuthnChallengePK(challengeID)
	item, err := s.getJSONItem(ctx, pk, pk)
	if err != nil {
		return model.WebAuthnChallenge{}, err
	}
	var c model.WebAuthnChallenge
	if err := json.Unmarshal([]byte(item.Data), &c); err != nil {
		return model.WebAuthnChallenge{}, err
	}
	return c, nil
}

func (s *DynamoStore) DeleteWebAuthnChallenge(ctx context.Context, challengeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	pk := webAuthnChallengePK(challengeID)
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: pk},
			"sk": &types.AttributeValueMemberS{Value: pk},
		},
	})
	return err
}

func (s *DynamoStore) PutWebAuthnCredential(ctx context.Context, c model.WebAuthnCredential) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	sk := webAuthnCredSK(c.CredentialID)
	if err := s.putJSONItem(ctx, userPK(c.UserID), sk, "wacred", string(data), 0); err != nil {
		return err
	}
	return s.putJSONItem(ctx, pkWebAuthnCredList, sk, "wacredlist", string(data), 0)
}

func (s *DynamoStore) GetWebAuthnCredential(ctx context.Context, credentialID string) (model.WebAuthnCredential, error) {
	if err := ctx.Err(); err != nil {
		return model.WebAuthnCredential{}, err
	}
	sk := webAuthnCredSK(credentialID)
	item, err := s.getJSONItem(ctx, pkWebAuthnCredList, sk)
	if err != nil {
		return model.WebAuthnCredential{}, err
	}
	var c model.WebAuthnCredential
	if err := json.Unmarshal([]byte(item.Data), &c); err != nil {
		return model.WebAuthnCredential{}, err
	}
	return c, nil
}

func (s *DynamoStore) ListWebAuthnCredentials(ctx context.Context) ([]model.WebAuthnCredential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        &types.AttributeValueMemberS{Value: pkWebAuthnCredList},
			":sk_prefix": &types.AttributeValueMemberS{Value: "WACRED#"},
		},
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.WebAuthnCredential, 0, len(result.Items))
	for _, raw := range result.Items {
		var item dynamoItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			return nil, err
		}
		var c model.WebAuthnCredential
		if err := json.Unmarshal([]byte(item.Data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *DynamoStore) ListWebAuthnCredentialsByUser(ctx context.Context, userID string) ([]model.WebAuthnCredential, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        &types.AttributeValueMemberS{Value: userPK(userID)},
			":sk_prefix": &types.AttributeValueMemberS{Value: "WACRED#"},
		},
	})
	if err != nil {
		return nil, err
	}
	out := make([]model.WebAuthnCredential, 0, len(result.Items))
	for _, raw := range result.Items {
		var item dynamoItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			return nil, err
		}
		var c model.WebAuthnCredential
		if err := json.Unmarshal([]byte(item.Data), &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (s *DynamoStore) DeleteAllWebAuthnCredentials(ctx context.Context, userID string) error {
	creds, err := s.ListWebAuthnCredentialsByUser(ctx, userID)
	if err != nil {
		return err
	}
	for _, c := range creds {
		sk := webAuthnCredSK(c.CredentialID)
		_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
				"sk": &types.AttributeValueMemberS{Value: sk},
			},
		})
		if err != nil {
			return err
		}
		_, err = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: pkWebAuthnCredList},
				"sk": &types.AttributeValueMemberS{Value: sk},
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *DynamoStore) PutRefreshVault(ctx context.Context, v model.RefreshVault) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.putJSONItem(ctx, userPK(v.UserID), refreshVaultSK(), "warefresh", string(data), 0)
}

func (s *DynamoStore) GetRefreshVault(ctx context.Context, userID string) (model.RefreshVault, error) {
	if err := ctx.Err(); err != nil {
		return model.RefreshVault{}, err
	}
	item, err := s.getJSONItem(ctx, userPK(userID), refreshVaultSK())
	if err != nil {
		return model.RefreshVault{}, err
	}
	var v model.RefreshVault
	if err := json.Unmarshal([]byte(item.Data), &v); err != nil {
		return model.RefreshVault{}, err
	}
	return v, nil
}

func (s *DynamoStore) DeleteRefreshVault(ctx context.Context, userID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: userPK(userID)},
			"sk": &types.AttributeValueMemberS{Value: refreshVaultSK()},
		},
	})
	return err
}
