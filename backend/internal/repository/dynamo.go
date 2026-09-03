package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

// DynamoAPI is the seam that makes DynamoStore testable. The v1 store held a
// concrete *dynamodb.Client, which made the whole type untestable by
// construction.
type DynamoAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// DynamoStore implements Store using DynamoDB with a single-table design.
//
// Keys: pk = USER#<tenantId>, sk = SETTINGS | NOTE#<id> | CAPTURE#<id> |
// IDEM#<key> | WACRED#<id> | WAREFRESH.
//
// GSI1 (gsi1pk = TENANT#<tenantId>#NOTE#<noteId>, gsi1sk = CAPTURE#<createdAt>)
// turns note -> captures into a direct query.
//
// Attributes that a list renders or filters on are stored top-level so queries
// can use a ProjectionExpression. The `data` blob is retained as the full
// record, but it is never transferred by a list.
type DynamoStore struct {
	client    DynamoAPI
	tableName string
	indexName string
}

// The real store has to satisfy the whole interface, not merely the parts the
// tests reach. A missing method here is a method the API silently falls back
// away from at runtime rather than failing to build.
var (
	_ Store   = (*DynamoStore)(nil)
	_ Objects = (*S3Objects)(nil)
)

// gsi1Name and gsi2Name are the indexes the template creates.
const (
	gsi1Name = "gsi1"
	gsi2Name = "gsi2"
)

// NewDynamoStore creates a new DynamoDB-backed store.
func NewDynamoStore(client DynamoAPI, tableName string) *DynamoStore {
	return &DynamoStore{
		client:    client,
		tableName: tableName,
		indexName: gsi1Name,
	}
}

// maxFilterRounds bounds how many Query round trips one page may cost when a
// FilterExpression discards most items, so a list can never run unbounded.
const maxFilterRounds = 10

// Key mapping helpers for single-table design
func userPK(tenantID string) string {
	return fmt.Sprintf("USER#%s", tenantID)
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

func idemSK(key string) string {
	return "IDEM#" + key
}

// noteCapturesGSI1PK is the GSI1 partition holding one note's captures. The
// prefix is TENANT#, matching the CloudFormation template exactly.
func noteCapturesGSI1PK(tenantID, noteID string) string {
	return fmt.Sprintf("TENANT#%s#NOTE#%s", tenantID, noteID)
}

func captureGSI1SK(createdAt string) string {
	return "CAPTURE#" + createdAt
}

// GSI2 orders a tenant's notes by when they were last touched.
//
// The base table cannot answer this. Its sort key is NOTE#<id> and a note id
// leads with its CREATION instant, so walking it backwards returns the most
// recently created note — and in a voice app the common case is a note made
// months ago that a capture appended to this morning. Ordering by update time
// needs the update time in a key, which is what this index is.
//
// The shelf is in the partition rather than in a filter. DynamoDB applies Limit
// before FilterExpression, so a filtered query returns short pages and has to
// be re-issued; with active and archived in separate partitions each list is a
// plain query over exactly the items it wants, and neither can page the other
// out.
const (
	noteShelfActive   = "ACTIVE"
	noteShelfArchived = "ARCHIVED"
)

// NoteIndexKeys returns the GSI2 key attributes a note item must carry.
//
// Exported so the backfill in chintanctl derives them from the same code the
// write path uses. Two copies of this derivation is how an index quietly
// disagrees with the table it indexes: the shelf choice and the timestamp
// normalisation both have to match exactly, and neither failure would error —
// a note would simply stop appearing in a list.
func NoteIndexKeys(tenantID string, n model.NoteIndex) (pk, sk string) {
	return notesShelfGSI2PK(tenantID, noteShelfOf(n)), noteUpdatedSortKey(n.UpdatedAt)
}

func notesShelfGSI2PK(tenantID, shelf string) string {
	return fmt.Sprintf("TENANT#%s#NOTES#%s", tenantID, shelf)
}

// noteShelfOf reports which shelf a note belongs on. Archived is "has a
// deletion stamp"; whether it has also passed its purge deadline is a question
// about time, which cannot live in a key, so the archived list still filters on
// purge_after_epoch.
func noteShelfOf(n model.NoteIndex) string {
	if strings.TrimSpace(n.DeletedAt) == "" {
		return noteShelfActive
	}
	return noteShelfArchived
}

// noteUpdatedSortKey renders a note's update time as a fixed-width instant.
//
// The width is load-bearing. model.TimeLayout pads the fraction to nine digits
// precisely so a string comparison is a chronological comparison; RFC3339Nano
// trims trailing zeros, so "…:00Z" sorts ABOVE "…:00.1Z" because 'Z' > '.'.
// That defect was found and fixed in the capture router, where it handed the
// model the wrong fifty notes. A sort key built from a variable-width timestamp
// reintroduces it silently and permanently, because a GSI sort key cannot be
// changed without rebuilding the index.
//
// A value written before the fixed-width layout existed is re-rendered here. An
// unparseable one is carried through as-is: it sorts oddly, which is visible,
// rather than being dropped from the index, which is not.
func noteUpdatedSortKey(updatedAt string) string {
	if t, err := model.ParseTime(updatedAt); err == nil {
		return model.FormatTime(t)
	}
	return updatedAt
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

// dynamoItem represents the generic JSON-blob item used by records that have no
// queryable attributes of their own (settings, challenges, credentials, vault).
type dynamoItem struct {
	PK   string `dynamodbav:"pk"`
	SK   string `dynamodbav:"sk"`
	Type string `dynamodbav:"type"`
	Data string `dynamodbav:"data"` // JSON-encoded model data
	TTL  int64  `dynamodbav:"ttl,omitempty"`
}

// The note list no longer names its attributes. It reads gsi2, whose INCLUDE
// projection is that list — `snippet` included, because note matching scores
// against it and dropping it to save bytes would silently degrade routing — and
// `data` excluded, which is the transfer cost the projection work removed. A
// ProjectionExpression here would be a second copy of that list to keep in
// step, and on a GSI it cannot fetch what the index does not hold: it would
// return blanks rather than fail.

func strAttr(v string) types.AttributeValue { return &types.AttributeValueMemberS{Value: v} }

func numAttr(v int64) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", v)}
}

func boolAttr(v bool) types.AttributeValue { return &types.AttributeValueMemberBOOL{Value: v} }

func strListAttr(v []string) types.AttributeValue {
	list := make([]types.AttributeValue, 0, len(v))
	for _, s := range v {
		list = append(list, strAttr(s))
	}
	return &types.AttributeValueMemberL{Value: list}
}

func readString(m map[string]types.AttributeValue, name string) string {
	if v, ok := m[name].(*types.AttributeValueMemberS); ok {
		return v.Value
	}
	return ""
}

func readInt(m map[string]types.AttributeValue, name string) int64 {
	v, ok := m[name].(*types.AttributeValueMemberN)
	if !ok {
		return 0
	}
	var out int64
	if _, err := fmt.Sscanf(v.Value, "%d", &out); err != nil {
		return 0
	}
	return out
}

func readBool(m map[string]types.AttributeValue, name string) bool {
	if v, ok := m[name].(*types.AttributeValueMemberBOOL); ok {
		return v.Value
	}
	return false
}

func readStrings(m map[string]types.AttributeValue, name string) []string {
	switch v := m[name].(type) {
	case *types.AttributeValueMemberL:
		out := make([]string, 0, len(v.Value))
		for _, item := range v.Value {
			if s, ok := item.(*types.AttributeValueMemberS); ok {
				out = append(out, s.Value)
			}
		}
		return out
	case *types.AttributeValueMemberSS:
		return append([]string(nil), v.Value...)
	}
	return nil
}

func isConditionalCheckFailed(err error) bool {
	var condErr *types.ConditionalCheckFailedException
	return errors.As(err, &condErr)
}

// conditionFailureItem returns the item DynamoDB echoed back with a failed
// condition, when the request asked for it.
func conditionFailureItem(err error) map[string]types.AttributeValue {
	var condErr *types.ConditionalCheckFailedException
	if errors.As(err, &condErr) {
		return condErr.Item
	}
	return nil
}

// ---------------------------------------------------------------- settings

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

// ---------------------------------------------------------------- notes

func noteItemAttrs(tenantID string, n model.NoteIndex) (map[string]types.AttributeValue, error) {
	blob, err := json.Marshal(n)
	if err != nil {
		return nil, fmt.Errorf("dynamo encode note: %w", err)
	}
	item := map[string]types.AttributeValue{
		"pk":              strAttr(userPK(tenantID)),
		"sk":              strAttr(noteSK(n.ID)),
		"type":            strAttr("note"),
		"note_id":         strAttr(n.ID),
		"title":           strAttr(n.Title),
		"aliases":         strListAttr(n.Aliases),
		"tags":            strListAttr(n.Tags),
		"snippet":         strAttr(n.Snippet),
		"created_at":      strAttr(n.CreatedAt),
		"updated_at":      strAttr(n.UpdatedAt),
		"s3_markdown_key": strAttr(n.S3MarkdownKey),
		"s3_meta_key":     strAttr(n.S3MetaKey),
		"deleted_at":      strAttr(n.DeletedAt),
		"purge_after":     strAttr(n.PurgeAfter),
		"verbatim":        boolAttr(n.Verbatim),
		"version":         numAttr(n.Version),
		"data":            strAttr(string(blob)),
	}
	// GSI2: the notes list, ordered by when each note was last touched.
	item["gsi2pk"] = strAttr(notesShelfGSI2PK(tenantID, noteShelfOf(n)))
	item["gsi2sk"] = strAttr(noteUpdatedSortKey(n.UpdatedAt))
	if n.PurgeAfterEpoch > 0 {
		item["purge_after_epoch"] = numAttr(n.PurgeAfterEpoch)
		// The table has one TTL attribute, `ttl`, shared with challenges and
		// idempotency records. purge_after_epoch is the projected, queryable
		// copy; ttl is what DynamoDB expires on.
		item["ttl"] = numAttr(n.PurgeAfterEpoch)
	}
	return item, nil
}

// noteFromItem rebuilds a note from a stored item.
//
// The record blob is decoded first and the promoted attributes are overlaid on
// top, the way captureFromItem does. Rebuilding from the promoted attributes
// alone is what silently destroyed `verbatim` and `created_at` on every read:
// a field that exists on the model but was never promoted read back as its zero
// value, and the next update wrote that zero into the blob permanently. With
// the overlay, a field nobody remembered to promote degrades to "not carried by
// a projection-only read" instead of "erased".
func noteFromItem(m map[string]types.AttributeValue) (model.NoteIndex, error) {
	var n model.NoteIndex
	if blob := readString(m, "data"); blob != "" {
		if err := json.Unmarshal([]byte(blob), &n); err != nil {
			return model.NoteIndex{}, fmt.Errorf("dynamo decode note: %w", err)
		}
	}
	if _, ok := m["note_id"]; !ok {
		// Item written before attributes were promoted: the blob is all there is.
		return n, nil
	}
	n.ID = readString(m, "note_id")
	n.Title = readString(m, "title")
	n.Aliases = readStrings(m, "aliases")
	n.Tags = readStrings(m, "tags")
	n.Snippet = readString(m, "snippet")
	n.CreatedAt = readString(m, "created_at")
	n.UpdatedAt = readString(m, "updated_at")
	n.S3MarkdownKey = readString(m, "s3_markdown_key")
	n.S3MetaKey = readString(m, "s3_meta_key")
	n.DeletedAt = readString(m, "deleted_at")
	n.PurgeAfter = readString(m, "purge_after")
	n.PurgeAfterEpoch = readInt(m, "purge_after_epoch")
	n.Version = readInt(m, "version")
	// Only overwrite what the read actually projected, so a partial projection
	// never blanks a field the blob already supplied.
	if _, ok := m["verbatim"]; ok {
		n.Verbatim = readBool(m, "verbatim")
	}
	if n.Aliases == nil {
		n.Aliases = []string{}
	}
	return n, nil
}

// listNotes runs the shared paginated note query. filter is applied server-side.
func (s *DynamoStore) listNotes(ctx context.Context, tenantID, shelf, filter string, extraValues map[string]types.AttributeValue, opts ListOptions) (Page[model.NoteIndex], error) {
	if err := ctx.Err(); err != nil {
		return Page[model.NoteIndex]{}, err
	}

	// GSI2, not the base table. The base table is keyed NOTE#<id> and a note id
	// leads with its creation instant, so it can only answer "newest made";
	// the owner asked for "most recently touched", which in a voice app is a
	// different note most of the time, because every capture appends to one.
	gsiPK := notesShelfGSI2PK(tenantID, shelf)
	start, err := decodeCursor(opts.Cursor, cursorScope{
		pkAttr: "gsi2pk", wantPK: gsiPK,
		// No sort-key prefix to check: the sort key is a timestamp and shares no
		// prefix. The shelf is already in the partition, so an archived cursor
		// cannot be replayed against the active list either.
		skAttr: "gsi2sk", skPrefix: "",
		direction: cursorDescending,
		// Every note cursor that existed before this index did was a base-table
		// cursor carrying pk/sk, so it fails the partition check above whatever
		// this says. Declared for the same reason the others are.
		unmarked: cursorDescending,
	})
	if err != nil {
		return Page[model.NoteIndex]{}, err
	}

	limit := opts.limit()
	items := make([]model.NoteIndex, 0, limit)

	// DynamoDB applies Limit before FilterExpression, so a page can come back
	// short while more matching items exist. Keep querying until the page is
	// full or the partition is exhausted; bound the rounds so a filter that
	// matches almost nothing cannot turn one request into an unbounded scan.
	// The active shelf carries no filter at all now, so this only spins for the
	// archived list, whose filter drops notes already past their purge deadline.
	for round := 0; round < maxFilterRounds && int32(len(items)) < limit; round++ {
		values := map[string]types.AttributeValue{
			":pk": strAttr(gsiPK),
		}
		for k, v := range extraValues {
			values[k] = v
		}

		in := &dynamodb.QueryInput{
			TableName:                 aws.String(s.tableName),
			IndexName:                 aws.String(gsi2Name),
			KeyConditionExpression:    aws.String("gsi2pk = :pk"),
			ExpressionAttributeValues: values,
			ExclusiveStartKey:         start,
			Limit:                     aws.Int32(limit - int32(len(items))),
			// Most recently touched first. This is the whole point of the index.
			ScanIndexForward: aws.Bool(false),
		}
		if filter != "" {
			in.FilterExpression = aws.String(filter)
		}
		// No ProjectionExpression. The index's INCLUDE projection is already
		// exactly the notes list, so naming the attributes again would be a
		// second copy of that list to keep in step with the template — and on a
		// GSI a ProjectionExpression cannot fetch what the index does not hold,
		// so it would silently return blanks rather than fail.

		out, err := s.client.Query(ctx, in)
		if err != nil {
			return Page[model.NoteIndex]{}, fmt.Errorf("dynamo query notes: %w", err)
		}

		for _, raw := range out.Items {
			if readString(raw, "note_id") == "" {
				// An item written before the attributes were promoted carries
				// only the `data` blob, which the index does not project. Read
				// it whole rather than dropping the note.
				n, err := s.GetNote(ctx, tenantID, trimPrefix(readString(raw, "sk"), "NOTE#"))
				if errors.Is(err, ErrNotFound) {
					continue
				}
				if err != nil {
					return Page[model.NoteIndex]{}, err
				}
				items = append(items, n)
				continue
			}
			n, err := noteFromItem(raw)
			if err != nil {
				return Page[model.NoteIndex]{}, err
			}
			items = append(items, n)
		}

		start = out.LastEvaluatedKey
		if len(start) == 0 {
			return Page[model.NoteIndex]{Items: items}, nil
		}
	}

	cursor, err := encodeCursor(start, cursorDescending)
	if err != nil {
		return Page[model.NoteIndex]{}, err
	}
	return Page[model.NoteIndex]{Items: items, Cursor: cursor}, nil
}

// activeNoteFilter keeps notes that were never archived. Items written before
// deleted_at was promoted carry no such attribute, so absence counts as active.
// ReindexNotes writes the GSI2 key attributes onto every note in a tenant's
// partition, and reports how many it touched.
//
// It exists because adding a global secondary index does NOT index the items
// already in the table. DynamoDB backfills a new index only from items that
// carry its key attributes, and every note written before this index existed
// carries neither — so on the deploy that adds it, the notes list goes empty
// and stays empty until each note is written again. That is a data-visibility
// outage, not a slow migration, and it is the reason this is shipped with the
// index rather than after it.
//
// It is idempotent and it does not touch `version`. A version bump would be a
// lost update waiting to happen: a client holding version 4 would find 5 in the
// table and be told to reconcile a change nobody made. This writes only the two
// index attributes, so an editor open in another tab is unaffected.
//
// It reads the whole item rather than the promoted attributes, so a note
// written before ANY attribute was promoted — one that is nothing but a `data`
// blob — is indexed from the blob rather than skipped.
func (s *DynamoStore) ReindexNotes(ctx context.Context, tenantID string) (int, error) {
	pk := userPK(tenantID)
	var start map[string]types.AttributeValue
	touched := 0

	for {
		if err := ctx.Err(); err != nil {
			return touched, err
		}
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":        strAttr(pk),
				":sk_prefix": strAttr("NOTE#"),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return touched, fmt.Errorf("dynamo reindex notes: %w", err)
		}

		for _, raw := range out.Items {
			n, err := noteFromItem(raw)
			if err != nil {
				return touched, fmt.Errorf("dynamo reindex notes: %w", err)
			}
			if _, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName: aws.String(s.tableName),
				Key: map[string]types.AttributeValue{
					"pk": raw["pk"],
					"sk": raw["sk"],
				},
				UpdateExpression: aws.String("SET gsi2pk = :gpk, gsi2sk = :gsk"),
				ExpressionAttributeValues: map[string]types.AttributeValue{
					":gpk": strAttr(notesShelfGSI2PK(tenantID, noteShelfOf(n))),
					":gsk": strAttr(noteUpdatedSortKey(n.UpdatedAt)),
				},
				// The row must still exist. A note purged while this runs is not
				// an error and must not be recreated as a key-only ghost.
				ConditionExpression: aws.String("attribute_exists(pk)"),
			}); err != nil {
				if isConditionalCheckFailed(err) {
					continue
				}
				return touched, fmt.Errorf("dynamo reindex notes: %w", err)
			}
			touched++
		}

		start = out.LastEvaluatedKey
		if len(start) == 0 {
			return touched, nil
		}
	}
}

// The active shelf needs no filter: being on it is what "not archived" means,
// and the shelf is chosen at write time. Only the archived list still filters,
// on the one part of the question that is about time rather than state — a note
// past its purge deadline is one TTL has not collected yet, and it must not
// appear in the archive the user is looking at.
const archivedNoteFilter = "purge_after_epoch > :now"

func (s *DynamoStore) ListNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error) {
	return s.listNotes(ctx, tenantID, noteShelfActive, "", nil, opts)
}

func (s *DynamoStore) ListArchivedNotes(ctx context.Context, tenantID string, opts ListOptions) (Page[model.NoteIndex], error) {
	return s.listNotes(ctx, tenantID, noteShelfArchived, archivedNoteFilter, map[string]types.AttributeValue{
		":now": numAttr(time.Now().Unix()),
	}, opts)
}

func (s *DynamoStore) GetNote(ctx context.Context, tenantID, noteID string) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
	})
	if err != nil {
		return model.NoteIndex{}, fmt.Errorf("dynamo get note: %w", err)
	}
	if result.Item == nil {
		return model.NoteIndex{}, ErrNotFound
	}
	return noteFromItem(result.Item)
}

// PutNote writes conditionally on the version the caller read. An unconditional
// PutItem is how v1 lost a voice append that landed while the editor was open.
func (s *DynamoStore) PutNote(ctx context.Context, tenantID string, note model.NoteIndex) (model.NoteIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.NoteIndex{}, err
	}

	expected := note.Version
	next := note
	next.Version = expected + 1

	item, err := noteItemAttrs(tenantID, next)
	if err != nil {
		return model.NoteIndex{}, err
	}
	if next.PurgeAfterEpoch == 0 {
		// Restoring a note must clear the expiry, not leave the table poised to
		// delete it.
		delete(item, "purge_after_epoch")
		delete(item, "ttl")
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(s.tableName),
		Item:                      item,
		ConditionExpression:       aws.String(versionCondition(expected)),
		ExpressionAttributeValues: map[string]types.AttributeValue{":expected": numAttr(expected)},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return model.NoteIndex{}, ErrVersionConflict
		}
		return model.NoteIndex{}, fmt.Errorf("dynamo put note: %w", err)
	}
	return next, nil
}

// versionCondition guards a write on the version the caller read. Expected 0
// also admits an item written before versioning existed, which carries no
// version attribute; one write later it does.
func versionCondition(expected int64) string {
	if expected == 0 {
		return "attribute_not_exists(pk) OR attribute_not_exists(version) OR version = :expected"
	}
	return "version = :expected"
}

func (s *DynamoStore) DeleteNote(ctx context.Context, tenantID, noteID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(noteSK(noteID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo delete note: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------- captures

func captureItemAttrs(c model.CaptureIndex) (map[string]types.AttributeValue, error) {
	blob, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("dynamo encode capture: %w", err)
	}
	// The S3 keys are top-level rather than only inside the blob because GSI1
	// projects them: a cascade delete has to unlink every artefact, and an
	// attribute that lives only in `data` is invisible to a projection, so the
	// index would be unable to answer and every list would pay a second read.
	item := map[string]types.AttributeValue{
		"pk":                strAttr(userPK(c.UserID)),
		"sk":                strAttr(captureSK(c.ID)),
		"type":              strAttr("capture"),
		"capture_id":        strAttr(c.ID),
		"note_id":           strAttr(c.NoteID),
		"status":            strAttr(string(c.Status)),
		"created_at":        strAttr(c.CreatedAt),
		"version":           numAttr(c.Version),
		"append_token":      strAttr(c.AppendToken),
		"append_claimed_at": numAttr(c.AppendClaimedAt),
		"appended_at":       numAttr(c.AppendedAt),
		"duration_ms":       numAttr(c.DurationMS),
		"mode":              strAttr(string(c.Mode)),
		"error":             strAttr(c.Error),
		"audio_key":         strAttr(c.AudioKey),
		"raw_key":           strAttr(c.RawKey),
		"routed_key":        strAttr(c.RoutedKey),
		"clean_key":         strAttr(c.CleanKey),
		"segments_key":      strAttr(c.SegmentsKey),
		"peaks_key":         strAttr(c.PeaksKey),
		"data":              strAttr(string(blob)),
	}
	// Indexed even when NoteID is empty. A capture awaiting disambiguation has
	// no destination note, and leaving it out of the index entirely is what made
	// ListCapturesByNote(tenant, "") — which the export walk relies on — return
	// nothing on DynamoDB while returning everything on the in-memory store.
	item["gsi1pk"] = strAttr(noteCapturesGSI1PK(c.UserID, c.NoteID))
	item["gsi1sk"] = strAttr(captureGSI1SK(c.CreatedAt))
	return item, nil
}

func captureFromItem(m map[string]types.AttributeValue) (model.CaptureIndex, error) {
	var c model.CaptureIndex
	if blob := readString(m, "data"); blob != "" {
		if err := json.Unmarshal([]byte(blob), &c); err != nil {
			return model.CaptureIndex{}, fmt.Errorf("dynamo decode capture: %w", err)
		}
	}
	if _, ok := m["capture_id"]; !ok {
		return c, nil
	}
	c.ID = readString(m, "capture_id")
	c.NoteID = readString(m, "note_id")
	c.Status = model.CaptureStatus(readString(m, "status"))
	c.CreatedAt = readString(m, "created_at")
	c.Version = readInt(m, "version")
	c.AppendToken = readString(m, "append_token")
	c.AppendClaimedAt = readInt(m, "append_claimed_at")
	c.AppendedAt = readInt(m, "appended_at")
	c.DurationMS = readInt(m, "duration_ms")
	c.AudioKey = readString(m, "audio_key")
	c.RawKey = readString(m, "raw_key")
	c.RoutedKey = readString(m, "routed_key")
	c.CleanKey = readString(m, "clean_key")
	c.SegmentsKey = readString(m, "segments_key")
	c.PeaksKey = readString(m, "peaks_key")
	// Only overwrite what the read actually projected, so a partial projection
	// never blanks a field the blob already supplied.
	if _, ok := m["mode"]; ok {
		c.Mode = model.CleanupMode(readString(m, "mode"))
	}
	if _, ok := m["error"]; ok {
		c.Error = readString(m, "error")
	}
	return c, nil
}

func (s *DynamoStore) PutCapture(ctx context.Context, capture model.CaptureIndex) (model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.CaptureIndex{}, err
	}

	expected := capture.Version
	next := capture
	next.Version = expected + 1

	item, err := captureItemAttrs(next)
	if err != nil {
		return model.CaptureIndex{}, err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:                 aws.String(s.tableName),
		Item:                      item,
		ConditionExpression:       aws.String(versionCondition(expected)),
		ExpressionAttributeValues: map[string]types.AttributeValue{":expected": numAttr(expected)},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return model.CaptureIndex{}, ErrVersionConflict
		}
		return model.CaptureIndex{}, fmt.Errorf("dynamo put capture: %w", err)
	}
	return next, nil
}

func (s *DynamoStore) GetCapture(ctx context.Context, tenantID, captureID string) (model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.CaptureIndex{}, err
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(captureSK(captureID)),
		},
	})
	if err != nil {
		return model.CaptureIndex{}, fmt.Errorf("dynamo get capture: %w", err)
	}
	if result.Item == nil {
		return model.CaptureIndex{}, ErrNotFound
	}
	c, err := captureFromItem(result.Item)
	if err != nil {
		return model.CaptureIndex{}, err
	}
	c.UserID = tenantID
	return c, nil
}

// ListCapturesByNote queries GSI1 for exactly this note's captures. v1 read the
// tenant's entire capture partition and filtered in Go, so cost grew with total
// captures rather than captures for this note.
//
// GSI1 is an INCLUDE projection — deliberately not ALL, because ALL would carry
// the `data` blob, the largest attribute on the item and the whole cost the
// projection exists to avoid. It carries what a capture list renders plus every
// S3 key a cascade delete has to unlink, so the query answers on its own and
// there is no second read per item.
//
// A field absent from gsi1NonKeyAttributes is absent from a listed capture. It
// is not free to add later: changing a GSI projection deletes and rebuilds the
// index.
func (s *DynamoStore) ListCapturesByNote(ctx context.Context, tenantID, noteID string, opts ListOptions) (Page[model.CaptureIndex], error) {
	if err := ctx.Err(); err != nil {
		return Page[model.CaptureIndex]{}, err
	}

	gsiPK := noteCapturesGSI1PK(tenantID, noteID)
	start, err := decodeCursor(opts.Cursor, cursorScope{
		pkAttr: "gsi1pk", wantPK: gsiPK, skAttr: "gsi1sk", skPrefix: "CAPTURE#",
		// Already newest first, and unchanged by the notes reversal — so a
		// cursor minted before directions were recorded is a descending one and
		// is still good.
		direction: cursorDescending, unmarked: cursorDescending,
	})
	if err != nil {
		return Page[model.CaptureIndex]{}, err
	}

	limit := opts.limit()
	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		IndexName:              aws.String(s.indexName),
		KeyConditionExpression: aws.String("gsi1pk = :pk AND begins_with(gsi1sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        strAttr(gsiPK),
			":sk_prefix": strAttr("CAPTURE#"),
		},
		// Newest first: gsi1sk is CAPTURE#<createdAt> with a fixed-width
		// timestamp, so descending index order is reverse chronological order.
		ScanIndexForward:  aws.Bool(false),
		ExclusiveStartKey: start,
		Limit:             aws.Int32(limit),
	})
	if err != nil {
		return Page[model.CaptureIndex]{}, fmt.Errorf("dynamo query captures by note: %w", err)
	}

	captures := make([]model.CaptureIndex, 0, len(out.Items))
	for _, raw := range out.Items {
		if _, ok := raw["capture_id"]; !ok {
			// An index entry written before capture_id was projected carries
			// only the keys. Read it whole rather than returning a capture with
			// no S3 keys, which a cascade delete would silently skip over.
			full, err := s.GetCapture(ctx, tenantID, trimPrefix(readString(raw, "sk"), "CAPTURE#"))
			if errors.Is(err, ErrNotFound) {
				continue // index entry outliving its item
			}
			if err != nil {
				return Page[model.CaptureIndex]{}, err
			}
			captures = append(captures, full)
			continue
		}
		c, err := captureFromItem(raw)
		if err != nil {
			return Page[model.CaptureIndex]{}, err
		}
		// pk is always projected, but the tenant is already known and is the
		// only value that could be correct here.
		c.UserID = tenantID
		captures = append(captures, c)
	}

	cursor, err := encodeCursor(out.LastEvaluatedKey, cursorDescending)
	if err != nil {
		return Page[model.CaptureIndex]{}, err
	}
	return Page[model.CaptureIndex]{Items: captures, Cursor: cursor}, nil
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// ListCaptures returns one page of every capture the tenant owns, newest first.
//
// This reads the base table rather than GSI1, and that is the point. GSI1
// partitions captures by their destination note, so a `needs_target` capture —
// one the router could not place, which is precisely the capture the user has
// to be shown in order to act on it — has no note to be indexed under.
// Assembling a tenant-wide list by walking notes therefore misses exactly the
// captures that most need to be found: durable, and from the UI's side
// indistinguishable from lost.
func (s *DynamoStore) ListCaptures(ctx context.Context, tenantID string, opts ListOptions) (Page[model.CaptureIndex], error) {
	if err := ctx.Err(); err != nil {
		return Page[model.CaptureIndex]{}, err
	}

	pk := userPK(tenantID)
	start, err := decodeCursor(opts.Cursor, cursorScope{
		pkAttr: "pk", wantPK: pk, skAttr: "sk", skPrefix: "CAPTURE#",
		direction: cursorDescending, unmarked: cursorDescending,
	})
	if err != nil {
		return Page[model.CaptureIndex]{}, err
	}

	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        strAttr(pk),
			":sk_prefix": strAttr("CAPTURE#"),
		},
		// Capture ids carry their creation instant as a fixed-width prefix, so
		// descending sort-key order is reverse chronological order and page one
		// holds the newest captures — which is what makes a progress card
		// showing in-flight work find them on the first request.
		ScanIndexForward:  aws.Bool(false),
		ExclusiveStartKey: start,
		Limit:             aws.Int32(opts.limit()),
	})
	if err != nil {
		return Page[model.CaptureIndex]{}, fmt.Errorf("dynamo query captures: %w", err)
	}

	captures := make([]model.CaptureIndex, 0, len(out.Items))
	for _, raw := range out.Items {
		c, err := captureFromItem(raw)
		if err != nil {
			return Page[model.CaptureIndex]{}, err
		}
		c.UserID = tenantID
		captures = append(captures, c)
	}
	sortCapturesNewestFirst(captures)

	cursor, err := encodeCursor(out.LastEvaluatedKey, cursorDescending)
	if err != nil {
		return Page[model.CaptureIndex]{}, err
	}
	return Page[model.CaptureIndex]{Items: captures, Cursor: cursor}, nil
}

// sortCapturesNewestFirst orders a page by creation time.
//
// Key order already delivers this for any capture created since ids became
// time-ordered, so in production this is a no-op. It exists so that a page
// containing an id from before that change is still returned in a sane order
// rather than in the arbitrary order of a random hex string.
func sortCapturesNewestFirst(captures []model.CaptureIndex) {
	sort.SliceStable(captures, func(i, j int) bool {
		if captures[i].CreatedAt != captures[j].CreatedAt {
			return captures[i].CreatedAt > captures[j].CreatedAt
		}
		return captures[i].ID > captures[j].ID
	})
}

func (s *DynamoStore) UpdateCaptureStatus(ctx context.Context, tenantID, captureID string, status model.CaptureStatus, errMsg string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	capture, err := s.GetCapture(ctx, tenantID, captureID)
	if err != nil {
		return err
	}
	capture.Status = status
	capture.Error = errMsg
	_, err = s.PutCapture(ctx, capture)
	return err
}

func (s *DynamoStore) DeleteCapture(ctx context.Context, tenantID, captureID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(captureSK(captureID)),
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return ErrNotFound
		}
		return fmt.Errorf("dynamo delete capture: %w", err)
	}
	return nil
}

// ---------------------------------------------------------- append guard

// appendClaimCondition admits a claim when nobody holds one, or when the holder
// abandoned it: an unfinished claim older than the lease is taken over so a
// worker that died mid-append cannot strand the capture forever.
const appendClaimCondition = "attribute_exists(pk) AND " +
	"(attribute_not_exists(append_token) OR append_token = :empty OR " +
	"(append_claimed_at < :stale AND (attribute_not_exists(appended_at) OR appended_at = :zero)))"

func (s *DynamoStore) ClaimCaptureAppend(ctx context.Context, tenantID, captureID, token string) (bool, model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return false, model.CaptureIndex{}, err
	}
	if token == "" {
		return false, model.CaptureIndex{}, errors.New("repository: empty append token")
	}

	current, err := s.GetCapture(ctx, tenantID, captureID)
	if err != nil {
		return false, model.CaptureIndex{}, err
	}

	now := time.Now()
	stale := now.Add(-AppendClaimLease).Unix()
	// Stated the way appendClaimCondition states it: a capture is claimable
	// when nobody holds it, or when the holder never finished and its lease has
	// expired. Reading it as "refuse unless not (unfinished and stale)" is the
	// same test turned inside out, and one negation harder to check against the
	// condition expression it has to agree with.
	claimable := current.AppendToken == "" ||
		(current.AppendedAt == 0 && current.AppendClaimedAt < stale)
	if !claimable {
		return false, current, nil
	}

	claimed := current
	claimed.AppendToken = token
	claimed.AppendClaimedAt = now.Unix()
	claimed.Version = current.Version + 1

	item, err := captureItemAttrs(claimed)
	if err != nil {
		return false, model.CaptureIndex{}, err
	}

	condition := appendClaimCondition + " AND (" + versionCondition(current.Version) + ")"
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                item,
		ConditionExpression: aws.String(condition),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":empty":    strAttr(""),
			":zero":     numAttr(0),
			":stale":    numAttr(stale),
			":expected": numAttr(current.Version),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			latest, getErr := s.GetCapture(ctx, tenantID, captureID)
			if getErr != nil {
				return false, model.CaptureIndex{}, getErr
			}
			return false, latest, nil
		}
		return false, model.CaptureIndex{}, fmt.Errorf("dynamo claim capture append: %w", err)
	}
	return true, claimed, nil
}

func (s *DynamoStore) CompleteCaptureAppend(ctx context.Context, tenantID, captureID, token string) (model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return model.CaptureIndex{}, err
	}

	current, err := s.GetCapture(ctx, tenantID, captureID)
	if err != nil {
		return model.CaptureIndex{}, err
	}
	if current.AppendToken != token {
		return model.CaptureIndex{}, ErrVersionConflict
	}
	if current.AppendedAt > 0 {
		return current, nil
	}

	done := current
	done.Status = model.StatusAppended
	done.Error = ""
	done.AppendedAt = time.Now().Unix()
	done.Version = current.Version + 1

	item, err := captureItemAttrs(done)
	if err != nil {
		return model.CaptureIndex{}, err
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                item,
		ConditionExpression: aws.String("append_token = :token AND (" + versionCondition(current.Version) + ")"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":token":    strAttr(token),
			":expected": numAttr(current.Version),
		},
	})
	if err != nil {
		if isConditionalCheckFailed(err) {
			return model.CaptureIndex{}, ErrVersionConflict
		}
		return model.CaptureIndex{}, fmt.Errorf("dynamo complete capture append: %w", err)
	}
	return done, nil
}

// ------------------------------------------------------------- idempotency

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
	return rec
}

func (s *DynamoStore) CompleteIdempotent(ctx context.Context, tenantID, key string, status int, response []byte) error {
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

// --------------------------------------------------------------- generic

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

// --------------------------------------------------------------- webauthn

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
			"pk": strAttr(pk),
			"sk": strAttr(pk),
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

// listCredentials is the shared paginated credential query.
func (s *DynamoStore) listCredentials(ctx context.Context, pk string, opts ListOptions) (Page[model.WebAuthnCredential], error) {
	if err := ctx.Err(); err != nil {
		return Page[model.WebAuthnCredential]{}, err
	}

	start, err := decodeCursor(opts.Cursor, cursorScope{
		pkAttr: "pk", wantPK: pk, skAttr: "sk", skPrefix: "WACRED#",
		direction: cursorAscending, unmarked: cursorAscending,
	})
	if err != nil {
		return Page[model.WebAuthnCredential]{}, err
	}

	out, err := s.client.Query(ctx, &dynamodb.QueryInput{
		TableName:              aws.String(s.tableName),
		KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk":        strAttr(pk),
			":sk_prefix": strAttr("WACRED#"),
		},
		ExclusiveStartKey: start,
		Limit:             aws.Int32(opts.limit()),
	})
	if err != nil {
		return Page[model.WebAuthnCredential]{}, err
	}

	creds := make([]model.WebAuthnCredential, 0, len(out.Items))
	for _, raw := range out.Items {
		var item dynamoItem
		if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
			return Page[model.WebAuthnCredential]{}, err
		}
		var c model.WebAuthnCredential
		if err := json.Unmarshal([]byte(item.Data), &c); err != nil {
			return Page[model.WebAuthnCredential]{}, err
		}
		creds = append(creds, c)
	}

	cursor, err := encodeCursor(out.LastEvaluatedKey, cursorAscending)
	if err != nil {
		return Page[model.WebAuthnCredential]{}, err
	}
	return Page[model.WebAuthnCredential]{Items: creds, Cursor: cursor}, nil
}

func (s *DynamoStore) ListWebAuthnCredentials(ctx context.Context, opts ListOptions) (Page[model.WebAuthnCredential], error) {
	return s.listCredentials(ctx, pkWebAuthnCredList, opts)
}

func (s *DynamoStore) ListWebAuthnCredentialsByUser(ctx context.Context, tenantID string, opts ListOptions) (Page[model.WebAuthnCredential], error) {
	return s.listCredentials(ctx, userPK(tenantID), opts)
}

func (s *DynamoStore) DeleteAllWebAuthnCredentials(ctx context.Context, tenantID string) error {
	creds, err := DrainPages(ctx, 0, func(ctx context.Context, opts ListOptions) (Page[model.WebAuthnCredential], error) {
		return s.ListWebAuthnCredentialsByUser(ctx, tenantID, opts)
	})
	if err != nil {
		return err
	}
	for _, c := range creds {
		sk := webAuthnCredSK(c.CredentialID)
		_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": strAttr(userPK(tenantID)),
				"sk": strAttr(sk),
			},
		})
		if err != nil {
			return err
		}
		_, err = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": strAttr(pkWebAuthnCredList),
				"sk": strAttr(sk),
			},
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// ------------------------------------------------------------ refresh vault

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

func (s *DynamoStore) GetRefreshVault(ctx context.Context, tenantID string) (model.RefreshVault, error) {
	if err := ctx.Err(); err != nil {
		return model.RefreshVault{}, err
	}
	item, err := s.getJSONItem(ctx, userPK(tenantID), refreshVaultSK())
	if err != nil {
		return model.RefreshVault{}, err
	}
	var v model.RefreshVault
	if err := json.Unmarshal([]byte(item.Data), &v); err != nil {
		return model.RefreshVault{}, err
	}
	return v, nil
}

func (s *DynamoStore) DeleteRefreshVault(ctx context.Context, tenantID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(refreshVaultSK()),
		},
	})
	return err
}
