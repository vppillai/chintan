package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

// DynamoAPI is the seam that makes DynamoStore testable. Holding a concrete
// *dynamodb.Client would make the whole type untestable by construction.
type DynamoAPI interface {
	GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	PutItem(ctx context.Context, in *dynamodb.PutItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	DeleteItem(ctx context.Context, in *dynamodb.DeleteItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
	Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	BatchGetItem(ctx context.Context, in *dynamodb.BatchGetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.BatchGetItemOutput, error)
}

// DynamoStore implements Store using DynamoDB with a single-table design.
//
// Keys: pk = USER#<tenantId>, sk = SETTINGS | NOTE#<id> | CAPTURE#<id> |
// IDEM#<key> | ASK#<id>. One partition is not a tenant's: pk = INSTANCE holds
// sk = SPEND#<day>, the instance-wide daily provider spend counter
// (internal/pipeline DynamoCounter), written by the worker's breaker and read
// by the API's spend gate, and sk = AWSCOST#<yyyy-mm>, the month's AWS spend
// as last read from the stack's budget (internal/usage), written by the
// worker's daily aws-cost task and read by GET /v1/usage.
//
// GSI1 (gsi1pk = TENANT#<tenantId>#NOTE#<noteId>, gsi1sk = CAPTURE#<createdAt>)
// turns note -> captures into a direct query. It is the only index: the notes
// list reads the base table and orders in Go (see listNotes).
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

// gsi1Name is the one index the template creates.
const gsi1Name = "gsi1"

// NewDynamoStore creates a new DynamoDB-backed store.
func NewDynamoStore(client DynamoAPI, tableName string) *DynamoStore {
	return &DynamoStore{
		client:    client,
		tableName: tableName,
		indexName: gsi1Name,
	}
}

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

func askSK(askID string) string {
	return "ASK#" + askID
}

func deviceSK(deviceID string) string {
	return "DEVICE#" + deviceID
}

func pushSubscriptionSK(id string) string {
	return "PUSHSUB#" + id
}

// deviceKeyGSI1PK is the GSI1 partition a live device key is found under. The
// index is sparse: a revoked row carries no gsi1 keys, so the lookup cannot
// see it.
func deviceKeyGSI1PK(keyID string) string {
	return "DEVICEKEY#" + keyID
}

const deviceKeyGSI1SK = "KEY"

// noteCapturesGSI1PK is the GSI1 partition holding one note's captures. The
// prefix is TENANT#, matching the CloudFormation template exactly.
func noteCapturesGSI1PK(tenantID, noteID string) string {
	return fmt.Sprintf("TENANT#%s#NOTE#%s", tenantID, noteID)
}

func captureGSI1SK(createdAt string) string {
	return "CAPTURE#" + createdAt
}

// ttlGraceSeconds is how long after its purge deadline a note's row is left
// for DynamoDB TTL to collect: fourteen days.
//
// The row must outlive the deadline because the objects it names have to go
// first. The weekly expiry sweep (internal/purge) finds notes past their
// purge_after_epoch, runs PurgeNoteArtifacts — captures, audio, transcripts,
// the note body — and then deletes the row itself. TTL is the backstop for a
// sweep that did not run, and it has to lose the race to the sweep: TTL fires
// within about two days of the attribute's value, so a ttl equal to the
// deadline would usually delete the row before the weekly sweep had seen it,
// leaving every object the row named orphaned in the bucket — the leak the
// stream-triggered expiry Lambda used to exist to close. Two weeks gives the
// sweep two chances. The archived list filters on purge_after_epoch, not ttl,
// so a note past its deadline is already invisible while it waits.
const ttlGraceSeconds = 14 * 24 * 60 * 60

// noteListProjection is what a list reads of each note: everything a note row
// renders, what the editor and the archive need without a second read, and the
// archive's filter attribute. `snippet` is included because note matching
// scores against it and dropping it to save bytes would silently degrade
// routing. `data` — the blob that duplicates every attribute here — is the
// transfer cost this projection exists to avoid, and `sk` is kept so an item
// written before the attributes were promoted can be read whole by its key.
const noteListProjection = "sk, note_id, title, aliases, tags, snippet, created_at, updated_at, " +
	"s3_markdown_key, s3_meta_key, deleted_at, purge_after, purge_after_epoch, verbatim, #lang, version, " +
	"auto_clean, clean_mode, cleaned_mode, cleaned_at, cleaned_stale, cleaned_error, " +
	"cleaned_requested_at, cleaned_requested_mode, appending_capture, appending_at, kind, pinned_at, pin_rank"

// languageAttr is the promoted attribute for NoteIndex.Language. `language` is
// a DynamoDB reserved word, so the projection names it through an expression
// attribute name (#lang) rather than directly.
const languageAttr = "language"

// searchTextAttr is the promoted attribute holding NoteIndex.SearchText. It is
// not in noteListProjection: at up to 32 KB per note it would multiply the
// transfer of every notes list by an order of magnitude, and only search and
// the offline corpus read it. A list asks for it explicitly
// (ListOptions.IncludeSearchText).
const searchTextAttr = "search_text"

// cleanedBodyAttr is the promoted attribute holding NoteIndex.CleanedBody, the
// whole-note cleaned view. Same treatment as search_text and for a stronger
// reason: it is up to 200 KB per note, the blob must not duplicate it, and a
// list projects it only when asked (ListOptions.IncludeCleanedBody). The
// small fields that describe it — auto_clean, clean_mode, cleaned_mode,
// cleaned_at, cleaned_stale, cleaned_error, and the clean request's stamp
// cleaned_requested_at and cleaned_requested_mode — are promoted AND
// projected, so a listed note says truthfully whether it has a view, whether
// it is stale, and whether a run is in flight.
const cleanedBodyAttr = "cleaned_body"

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

// versionCondition guards a write on the version the caller read. Expected 0
// also admits an item written before versioning existed, which carries no
// version attribute; one write later it does.
func versionCondition(expected int64) string {
	if expected == 0 {
		return "attribute_not_exists(pk) OR attribute_not_exists(version) OR version = :expected"
	}
	return "version = :expected"
}

func trimPrefix(s, prefix string) string {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):]
	}
	return s
}

// batchGetKeys is DynamoDB's ceiling on keys per BatchGetItem.
const batchGetKeys = 100

// batchGet reads the named keys with projection, in batches of batchGetKeys,
// re-asking for whatever DynamoDB left unprocessed. Keys that name no item
// are simply absent from the result.
func (s *DynamoStore) batchGet(ctx context.Context, keys []map[string]types.AttributeValue, projection string, names map[string]string) ([]map[string]types.AttributeValue, error) {
	var out []map[string]types.AttributeValue
	for len(keys) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(batchGetKeys, len(keys))
		request := map[string]types.KeysAndAttributes{
			s.tableName: {Keys: keys[:n], ProjectionExpression: aws.String(projection), ExpressionAttributeNames: names},
		}
		keys = keys[n:]
		for len(request) > 0 {
			res, err := s.client.BatchGetItem(ctx, &dynamodb.BatchGetItemInput{RequestItems: request})
			if err != nil {
				return nil, err
			}
			out = append(out, res.Responses[s.tableName]...)
			request = res.UnprocessedKeys
			if pending, ok := request[s.tableName]; ok && len(pending.Keys) == 0 {
				request = nil
			}
		}
	}
	return out, nil
}

// --------------------------------------------------------------- note row

// NoteItemAttributes is the item PutNote writes for n, and CaptureItemAttributes
// the item PutCapture writes for c. They are exported for one caller:
// `chintanctl reconcile`, which repairs a row written before these attributes
// were promoted (August 2026) by writing the promoted set back onto it. The
// command could spell the attribute names itself, and did for search_text; a
// second spelling of thirty names is thirty ways for the store and its repair
// tool to disagree, and this is how they agree by construction.
func NoteItemAttributes(tenantID string, n model.NoteIndex) (map[string]types.AttributeValue, error) {
	return noteItemAttrs(tenantID, n)
}

// CaptureItemAttributes is NoteItemAttributes for a capture row.
func CaptureItemAttributes(c model.CaptureIndex) (map[string]types.AttributeValue, error) {
	return captureItemAttrs(c)
}

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
		"auto_clean":      boolAttr(n.AutoClean),
		"clean_mode":      strAttr(string(n.CleanMode)),
		"cleaned_mode":    strAttr(string(n.CleanedMode)),
		"cleaned_at":      strAttr(n.CleanedAt),
		"cleaned_stale":   boolAttr(n.CleanedStale),
		"cleaned_error":   strAttr(n.CleanedError),
		"data":            strAttr(string(blob)),

		"cleaned_requested_at":   strAttr(n.CleanedRequestedAt),
		"cleaned_requested_mode": strAttr(string(n.CleanedRequestedMode)),
		"appending_capture":      strAttr(n.AppendingCapture),
		"appending_at":           strAttr(n.AppendingAt),
	}
	if n.Language != "" {
		item[languageAttr] = strAttr(n.Language)
	}
	if n.Kind != "" {
		// Like language: written only when set, so a plain note's row carries no
		// attribute and reads back as the zero value the model means by it.
		item["kind"] = strAttr(n.Kind)
	}
	if n.PinnedAt != "" {
		// Written together and only when pinned, like kind: an unpinned row
		// carries neither attribute and reads as the zero value.
		item["pinned_at"] = strAttr(n.PinnedAt)
		item["pin_rank"] = numAttr(n.PinRank)
	}
	if n.SearchText != "" {
		// Promoted only. The blob deliberately omits it (model.NoteIndex tags it
		// json:"-"), so the field is stored once, not twice.
		item[searchTextAttr] = strAttr(n.SearchText)
	}
	if n.CleanedBody != "" {
		// Promoted only, as search_text is.
		item[cleanedBodyAttr] = strAttr(n.CleanedBody)
	}
	if n.PurgeAfterEpoch > 0 {
		item["purge_after_epoch"] = numAttr(n.PurgeAfterEpoch)
		// The table has one TTL attribute, `ttl`, shared with idempotency
		// records. purge_after_epoch is the deadline the archived list filters
		// on and the expiry sweep acts on; ttl is the backstop DynamoDB expires
		// on, deliberately later — see ttlGraceSeconds.
		item["ttl"] = numAttr(n.PurgeAfterEpoch + ttlGraceSeconds)
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
	if _, ok := m[searchTextAttr]; ok {
		n.SearchText = readString(m, searchTextAttr)
	}
	if _, ok := m[languageAttr]; ok {
		n.Language = readString(m, languageAttr)
	}
	if _, ok := m["kind"]; ok {
		n.Kind = readString(m, "kind")
	}
	if _, ok := m["pinned_at"]; ok {
		n.PinnedAt = readString(m, "pinned_at")
		n.PinRank = readInt(m, "pin_rank")
	}
	if _, ok := m[cleanedBodyAttr]; ok {
		n.CleanedBody = readString(m, cleanedBodyAttr)
	}
	if _, ok := m["auto_clean"]; ok {
		n.AutoClean = readBool(m, "auto_clean")
	}
	if _, ok := m["clean_mode"]; ok {
		n.CleanMode = model.NoteCleanMode(readString(m, "clean_mode"))
	}
	if _, ok := m["cleaned_mode"]; ok {
		n.CleanedMode = model.NoteCleanMode(readString(m, "cleaned_mode"))
	}
	if _, ok := m["cleaned_at"]; ok {
		n.CleanedAt = readString(m, "cleaned_at")
	}
	if _, ok := m["cleaned_stale"]; ok {
		n.CleanedStale = readBool(m, "cleaned_stale")
	}
	if _, ok := m["cleaned_error"]; ok {
		n.CleanedError = readString(m, "cleaned_error")
	}
	if _, ok := m["cleaned_requested_at"]; ok {
		n.CleanedRequestedAt = readString(m, "cleaned_requested_at")
	}
	if _, ok := m["cleaned_requested_mode"]; ok {
		n.CleanedRequestedMode = model.NoteCleanMode(readString(m, "cleaned_requested_mode"))
	}
	// The append stamp is written by UpdateItem, so the blob can be behind the
	// promoted attribute in both directions: carrying a stamp the row no longer
	// has (ClearNoteAppend REMOVEs the attribute) or lacking one it does. The
	// attribute is the truth when the read projected it, and its absence from a
	// full read means the stamp is cleared, whatever the blob says.
	if _, projected := m["version"]; projected {
		n.AppendingCapture = readString(m, "appending_capture")
		n.AppendingAt = readString(m, "appending_at")
	}
	if n.Aliases == nil {
		n.Aliases = []string{}
	}
	return n, nil
}
