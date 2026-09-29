package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

// --------------------------------------------------------------- settings

func (s *DynamoStore) GetSettings(ctx context.Context, tenantID string) (model.Settings, error) {
	if err := ctx.Err(); err != nil {
		return model.Settings{}, err
	}

	item, err := s.getJSONItem(ctx, userPK(tenantID), settingsSK())
	if errors.Is(err, ErrNotFound) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return model.Settings{}, fmt.Errorf("dynamo get settings: %w", err)
	}

	var settings model.Settings
	if err := json.Unmarshal([]byte(item.Data), &settings); err != nil {
		return model.Settings{}, fmt.Errorf("dynamo decode settings: %w", err)
	}
	return settings, nil
}

func (s *DynamoStore) PutSettings(ctx context.Context, tenantID string, settings model.Settings) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("dynamo encode settings: %w", err)
	}
	return s.putJSONItem(ctx, userPK(tenantID), settingsSK(), "settings", string(data), 0)
}

// ------------------------------------------------------------ idempotency

func (s *DynamoStore) BeginIdempotent(ctx context.Context, tenantID, key, fingerprint string) (*IdemRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errors.New("repository: empty idempotency key")
	}

	// The attempt token is generated once, before the SDK's own retries. If a
	// PutItem is committed but its response is lost, the SDK retry gets
	// ConditionalCheckFailed against an item this caller wrote — the token is
	// what tells that apart from a genuine duplicate, so a caller cannot lock
	// itself out of its own key for the whole TTL.
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("repository: attempt token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	now := time.Now().Unix()
	// `ttl` is when DynamoDB may delete the item and is the full TTL from the
	// start, since deletion is lazy anyway. `idem_expires_at` is what the
	// condition below honours, and a bare claim is honoured only for the lease;
	// CompleteIdempotent extends it to the full TTL once a response exists.
	ttl := time.Now().Add(IdemTTL).Unix()
	claimExpires := time.Now().Add(IdemClaimLease).Unix()

	// There is deliberately no idem_response here. A claimed key has no response
	// yet, and absence is how that is spelled: an AttributeValue must carry
	// exactly one datatype, so a Binary member holding a nil slice is not "an
	// empty response", it is an attribute with no type at all, and DynamoDB
	// answers
	//   ValidationException: Supplied AttributeValue is empty, must contain
	//   exactly one of the supported datatypes
	// and refuses the whole PutItem. Every POST that carries an
	// Idempotency-Key — which is every capture — then 500s, so this one
	// attribute took the product down. idemFromItem already type-asserts the
	// read, so a missing attribute reads back as a nil response, which is what
	// it means.
	item := map[string]types.AttributeValue{
		"pk":                strAttr(userPK(tenantID)),
		"sk":                strAttr(idemSK(key)),
		"type":              strAttr("idem"),
		"idem_key":          strAttr(key),
		"idem_fingerprint":  strAttr(fingerprint),
		"idem_attempt":      strAttr(token),
		"idem_done":         &types.AttributeValueMemberBOOL{Value: false},
		"idem_status":       numAttr(0),
		"idem_claimed_at":   numAttr(now),
		"ttl":               numAttr(ttl),
		"idem_expires_at":   numAttr(claimExpires),
		"idem_tenant_scope": strAttr(tenantID),
	}

	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                           aws.String(s.tableName),
		Item:                                item,
		ConditionExpression:                 aws.String("attribute_not_exists(pk) OR idem_expires_at < :now"),
		ExpressionAttributeValues:           map[string]types.AttributeValue{":now": numAttr(now)},
		ReturnValuesOnConditionCheckFailure: types.ReturnValuesOnConditionCheckFailureAllOld,
	})
	if err == nil {
		return nil, nil // caller owns the key
	}
	if !isConditionalCheckFailed(err) {
		return nil, fmt.Errorf("dynamo begin idempotent: %w", err)
	}

	existing := conditionFailureItem(err)
	if existing == nil {
		got, getErr := s.client.GetItem(ctx, &dynamodb.GetItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": strAttr(userPK(tenantID)),
				"sk": strAttr(idemSK(key)),
			},
			ConsistentRead: aws.Bool(true),
		})
		if getErr != nil {
			return nil, fmt.Errorf("dynamo begin idempotent: %w", getErr)
		}
		if got.Item == nil {
			// The holder's record vanished between the failed condition and the
			// read. Treat as in-flight rather than guessing.
			return nil, ErrIdempotencyInFlight
		}
		existing = got.Item
	}

	rec := idemFromItem(existing)
	if rec.Fingerprint != fingerprint {
		return nil, ErrIdempotencyKeyReused
	}
	if readString(existing, "idem_attempt") == token {
		return nil, nil // our own committed write; we own the key
	}
	if rec.Done {
		return &rec, nil
	}
	return nil, ErrIdempotencyInFlight
}

func idemFromItem(m map[string]types.AttributeValue) IdemRecord {
	rec := IdemRecord{
		Key:         readString(m, "idem_key"),
		TenantID:    readString(m, "idem_tenant_scope"),
		Fingerprint: readString(m, "idem_fingerprint"),
		Status:      int(readInt(m, "idem_status")),
		ExpiresAt:   readInt(m, "idem_expires_at"),
	}
	if v, ok := m["idem_done"].(*types.AttributeValueMemberBOOL); ok {
		rec.Done = v.Value
	}
	if v, ok := m["idem_response"].(*types.AttributeValueMemberB); ok {
		rec.Response = v.Value
	}
	rec.ContentType = readString(m, "idem_content_type")
	return rec
}

func (s *DynamoStore) CompleteIdempotent(ctx context.Context, tenantID, key string, status int, contentType string, response []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Same rule as BeginIdempotent: a Binary attribute may not be empty, and an
	// empty one is rejected rather than stored. A completed response with no
	// body is a real case — a 204, or a handler that writes nothing — so the
	// attribute is left out of the SET instead of written empty. idem_done is
	// what records completion; idem_response only carries a body when there is
	// one.
	//
	// Completion is also when the record earns its full lifetime: the claim was
	// honoured only for IdemClaimLease, the recorded response for IdemTTL.
	update := "SET idem_done = :done, idem_status = :status, idem_expires_at = :expires"
	values := map[string]types.AttributeValue{
		":done":    &types.AttributeValueMemberBOOL{Value: true},
		":status":  numAttr(int64(status)),
		":expires": numAttr(time.Now().Add(IdemTTL).Unix()),
	}
	if len(response) > 0 {
		update += ", idem_response = :response"
		values[":response"] = &types.AttributeValueMemberB{Value: response}
	}
	if contentType != "" {
		update += ", idem_content_type = :content_type"
		values[":content_type"] = strAttr(contentType)
	}

	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(idemSK(key)),
		},
		UpdateExpression:          aws.String(update),
		ConditionExpression:       aws.String("attribute_exists(pk)"),
		ExpressionAttributeValues: values,
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo complete idempotent: %w", err)
	}
	return nil
}

func (s *DynamoStore) AbandonIdempotent(ctx context.Context, tenantID, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only an unfinished claim is deleted. A completed record is the answer a
	// replay is owed, and the condition is what stops a late abandon — say from
	// a request that 5xx'd after a concurrent SDK retry had committed — from
	// destroying it.
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(idemSK(key)),
		},
		ConditionExpression:       aws.String("attribute_exists(pk) AND idem_done = :notdone"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":notdone": &types.AttributeValueMemberBOOL{Value: false}},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return nil // nothing claimed, or already completed
		}
		return fmt.Errorf("dynamo abandon idempotent: %w", err)
	}
	return nil
}

// ----------------------------------------------------- push subscriptions

// pushSubscriptionItemAttrs is a push subscription row: every field a named
// attribute, like a device's, and no blob.
func pushSubscriptionItemAttrs(tenantID string, sub model.PushSubscription) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{
		"pk":              strAttr(userPK(tenantID)),
		"sk":              strAttr(pushSubscriptionSK(sub.ID)),
		"type":            strAttr("push_subscription"),
		"push_id":         strAttr(sub.ID),
		"endpoint":        strAttr(sub.Endpoint),
		"p256dh":          strAttr(sub.P256DH),
		"auth":            strAttr(sub.Auth),
		"label":           strAttr(sub.Label),
		"created_at":      strAttr(sub.CreatedAt),
		"last_success_at": strAttr(sub.LastSuccessAt),
		"failures":        numAttr(sub.Failures),
	}
}

func pushSubscriptionFromItem(tenantID string, m map[string]types.AttributeValue) model.PushSubscription {
	return model.PushSubscription{
		ID:            readString(m, "push_id"),
		TenantID:      tenantID,
		Endpoint:      readString(m, "endpoint"),
		P256DH:        readString(m, "p256dh"),
		Auth:          readString(m, "auth"),
		Label:         readString(m, "label"),
		CreatedAt:     readString(m, "created_at"),
		LastSuccessAt: readString(m, "last_success_at"),
		Failures:      readInt(m, "failures"),
	}
}

func (s *DynamoStore) PutPushSubscription(ctx context.Context, tenantID string, sub model.PushSubscription) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if sub.ID == "" {
		return errors.New("repository: push subscription without an id")
	}
	_, err := s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item:      pushSubscriptionItemAttrs(tenantID, sub),
	})
	if err != nil {
		return fmt.Errorf("dynamo put push subscription: %w", err)
	}
	return nil
}

func (s *DynamoStore) ListPushSubscriptions(ctx context.Context, tenantID string) ([]model.PushSubscription, error) {
	var start map[string]types.AttributeValue
	out := []model.PushSubscription{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		res, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":        strAttr(userPK(tenantID)),
				":sk_prefix": strAttr("PUSHSUB#"),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, fmt.Errorf("dynamo query push subscriptions: %w", err)
		}
		for _, raw := range res.Items {
			out = append(out, pushSubscriptionFromItem(tenantID, raw))
		}
		start = res.LastEvaluatedKey
		if len(start) == 0 {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// UpdatePushSubscriptionResult is one conditional UpdateItem on the two
// counters; a row the person removed meanwhile is left removed.
func (s *DynamoStore) UpdatePushSubscriptionResult(ctx context.Context, tenantID, id, lastSuccessAt string, failures int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(pushSubscriptionSK(id)),
		},
		UpdateExpression:    aws.String("SET last_success_at = :t, failures = :f"),
		ConditionExpression: aws.String("attribute_exists(pk)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t": strAttr(lastSuccessAt),
			":f": numAttr(failures),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo update push subscription result: %w", err)
	}
	return nil
}

// DeletePushSubscription is conditional on the row existing, so the API can
// answer 404 for an id the tenant never had rather than 204 for nothing.
func (s *DynamoStore) DeletePushSubscription(ctx context.Context, tenantID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(pushSubscriptionSK(id)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo delete push subscription: %w", err)
	}
	return nil
}

// ------------------------------------------------------------------- asks

// PutAsk stores the question as one JSON blob. Nothing queries an ask by
// anything but its key, so no attribute is promoted; the TTL is the row's
// expiry, which is the only lifecycle it has.
func (s *DynamoStore) PutAsk(ctx context.Context, tenantID string, a model.Ask) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a.ID == "" {
		return errors.New("repository: ask without an id")
	}
	blob, err := json.Marshal(a)
	if err != nil {
		return fmt.Errorf("dynamo encode ask: %w", err)
	}
	return s.putJSONItem(ctx, userPK(tenantID), askSK(a.ID), "ask", string(blob), a.ExpiresAt)
}

func (s *DynamoStore) GetAsk(ctx context.Context, tenantID, askID string) (model.Ask, error) {
	if err := ctx.Err(); err != nil {
		return model.Ask{}, err
	}
	item, err := s.getJSONItem(ctx, userPK(tenantID), askSK(askID))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return model.Ask{}, ErrNotFound
		}
		return model.Ask{}, fmt.Errorf("dynamo get ask: %w", err)
	}
	var a model.Ask
	if err := json.Unmarshal([]byte(item.Data), &a); err != nil {
		return model.Ask{}, fmt.Errorf("dynamo decode ask: %w", err)
	}
	return a, nil
}

// ---------------------------------------------------------------- generic

// dynamoItem represents the generic JSON-blob item used by records that have no
// queryable attributes of their own (settings).
type dynamoItem struct {
	PK   string `dynamodbav:"pk"`
	SK   string `dynamodbav:"sk"`
	Type string `dynamodbav:"type"`
	Data string `dynamodbav:"data"` // JSON-encoded model data
	TTL  int64  `dynamodbav:"ttl,omitempty"`
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
			"pk": strAttr(pk),
			"sk": strAttr(sk),
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
