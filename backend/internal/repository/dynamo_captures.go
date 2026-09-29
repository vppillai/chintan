package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/vppillai/chintan/backend/internal/model"
)

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
		"error":             strAttr(c.Error),
		// Top-level for the same reason as the keys: the by-note page reads
		// them back with one BatchGetItem (hydrateUnprojectedCaptureFields)
		// instead of decoding every row's blob. last_progress_at is what the
		// app's poll cadence and stuck rule run on; without it a regeneration
		// of a note older than ten minutes read as stuck from its first second
		// (QA 2026-09-27, F1).
		"source":           strAttr(c.Source),
		"last_progress_at": strAttr(c.LastProgressAt),
		"audio_key":        strAttr(c.AudioKey),
		"raw_key":          strAttr(c.RawKey),
		"routed_key":       strAttr(c.RoutedKey),
		"clean_key":        strAttr(c.CleanKey),
		"segments_key":     strAttr(c.SegmentsKey),
		"peaks_key":        strAttr(c.PeaksKey),
		"data":             strAttr(string(blob)),
	}
	// Indexed even when NoteID is empty. A capture awaiting disambiguation has
	// no destination note, and leaving it out of the index entirely is what made
	// ListCapturesByNote(tenant, "") — which the export walk relies on — return
	// nothing on DynamoDB while the in-memory store of the time returned
	// everything.
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
	if _, ok := m["source"]; ok {
		c.Source = readString(m, "source")
	}
	if _, ok := m["last_progress_at"]; ok {
		c.LastProgressAt = readString(m, "last_progress_at")
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

// ListCapturesByNote queries GSI1 for exactly this note's captures. Reading the
// tenant's entire capture partition and filtering in Go would make cost grow
// with total captures rather than captures for this note.
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
	if err := s.hydrateUnprojectedCaptureFields(ctx, tenantID, captures); err != nil {
		return Page[model.CaptureIndex]{}, err
	}

	cursor, err := encodeCursor(out.LastEvaluatedKey, cursorDescending)
	if err != nil {
		return Page[model.CaptureIndex]{}, err
	}
	return Page[model.CaptureIndex]{Items: captures, Cursor: cursor}, nil
}

// hydrateUnprojectedCaptureFields overlays the fields gsi1 does not project
// but a note's page needs: source, so the page can say which device sent a
// recording, and last_progress_at, so the app's poll cadence and stuck rule
// follow the pipeline rather than created_at (the capture read on its own
// always had both). One BatchGetItem per page, keyed off the projected ids; a
// row from before an attribute was promoted has nothing to say for it and
// keeps what the index gave.
//
// The overlay is the permanent answer at this scale, not a stopgap waiting
// for an index rebuild (round 6, R6-OD-2). CloudFormation cannot change a
// live index's projection, so projecting these two would mean a second index,
// a two-step deploy and a switch of the index-name constant, all to save one
// BatchGetItem per page of a note's recordings; that read costs well under a
// cent a month, and this function is sixty tested lines. Rebuild the index
// only when a DynamoDB change is needed for another reason, and fold these
// into its NonKeyAttributes and delete this then.
func (s *DynamoStore) hydrateUnprojectedCaptureFields(ctx context.Context, tenantID string, captures []model.CaptureIndex) error {
	if len(captures) == 0 {
		return nil
	}
	byID := make(map[string]int, len(captures))
	keys := make([]map[string]types.AttributeValue, 0, len(captures))
	for i, c := range captures {
		byID[c.ID] = i
		keys = append(keys, map[string]types.AttributeValue{
			"pk": strAttr(userPK(tenantID)),
			"sk": strAttr(captureSK(c.ID)),
		})
	}
	// SOURCE is a DynamoDB reserved word, hence the placeholder.
	items, err := s.batchGet(ctx, keys, "sk, #source, last_progress_at", map[string]string{"#source": "source"})
	if err != nil {
		return fmt.Errorf("dynamo hydrate capture fields: %w", err)
	}
	for _, item := range items {
		i, ok := byID[trimPrefix(readString(item, "sk"), "CAPTURE#")]
		if !ok {
			continue
		}
		if _, has := item["source"]; has {
			captures[i].Source = readString(item, "source")
		}
		if _, has := item["last_progress_at"]; has {
			captures[i].LastProgressAt = readString(item, "last_progress_at")
		}
	}
	return nil
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

// ListUnindexedCaptures is the base-table complement of ListCapturesByNote.
//
// GSI1 is sparse: an item without a gsi1pk attribute is simply not in it, and
// a capture row written before the index keys were promoted — August 2026,
// when a row was pk, sk, type and the `data` blob — is exactly such an item.
// Every query on the index is blind to it, including the one a note's purge
// uses to find the captures it must unlink, which is how "delete forever"
// left thirteen filed captures behind in production. Promoting the keys on
// read would repair the index for the next purge but not for this one, and
// would put a write inside a read; so the purge asks this once per note.
//
// The filter is on the attribute the index is keyed by, so what comes back is
// precisely the set the index cannot answer for. DynamoDB applies a
// FilterExpression after the read — the query pays for the whole CAPTURE#
// range of the partition in read units — but only the unindexed rows are
// transferred, and a tenant with none pays one empty page. Every page is
// followed: an unpaginated Query stops at 1 MB.
func (s *DynamoStore) ListUnindexedCaptures(ctx context.Context, tenantID string) ([]model.CaptureIndex, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var start map[string]types.AttributeValue
	captures := make([]model.CaptureIndex, 0)
	for {
		out, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			KeyConditionExpression: aws.String("pk = :pk AND begins_with(sk, :sk_prefix)"),
			FilterExpression:       aws.String("attribute_not_exists(gsi1pk)"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":pk":        strAttr(userPK(tenantID)),
				":sk_prefix": strAttr("CAPTURE#"),
			},
			ExclusiveStartKey: start,
		})
		if err != nil {
			return nil, fmt.Errorf("dynamo query unindexed captures: %w", err)
		}
		for _, raw := range out.Items {
			c, err := captureFromItem(raw)
			if err != nil {
				return nil, err
			}
			if c.ID == "" {
				// A blob without an id is still a row with a sort key.
				c.ID = trimPrefix(readString(raw, "sk"), "CAPTURE#")
			}
			c.UserID = tenantID
			captures = append(captures, c)
		}
		start = out.LastEvaluatedKey
		if len(start) == 0 {
			break
		}
	}
	sortCapturesNewestFirst(captures)
	return captures, nil
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

// ----------------------------------------------------------- append guard

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

// completeAppendAttempts bounds CompleteCaptureAppend's retries of a write
// lost to a concurrent writer of the same row. The writers a capture has are
// its holder and the odd duplicate delivery carrying the holder's token
// forward; more lost writes than that inside one call is not a race but
// something invoking the worker in a loop, and that should surface.
const completeAppendAttempts = 3

// CompleteCaptureAppend is idempotent for its own token. Two attempts at one
// append can both reach it: a duplicate delivery that finds the holder's
// paragraph already in the note finishes the bookkeeping beside the holder
// (pipeline.append), and one of the two then loses the conditional write
// below. A lost write is read again rather than reported, because the row
// says which of two things happened. Either the other attempt's completion
// landed, which is the outcome this call wanted, so that row is the answer;
// or a duplicate's `appending` status write moved the version with the token
// still standing — a duplicate that read the row after the claim carries the
// token forward in its own persist — which is the read-modify-write retry
// every other writer of a row makes. Only a row that no longer carries the
// token is a conflict. Before this the loser failed its invocation for
// nothing: an ERROR log, a CaptureStageFailures count, and a Lambda retry
// that found the capture terminal (R6-FL-2).
func (s *DynamoStore) CompleteCaptureAppend(ctx context.Context, tenantID, captureID, token string) (model.CaptureIndex, error) {
	for attempt := 1; ; attempt++ {
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

		now := time.Now()
		done := current
		done.Status = model.StatusAppended
		done.Error = ""
		done.AppendedAt = now.Unix()
		// The one status the pipeline's persist does not write, so the timing
		// record's last entry is stamped here.
		done.StageEntered(model.StatusAppended, model.FormatTime(now))
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
		if err == nil {
			return done, nil
		}
		if !isConditionalCheckFailed(err) {
			return model.CaptureIndex{}, fmt.Errorf("dynamo complete capture append: %w", err)
		}
		if attempt == completeAppendAttempts {
			return model.CaptureIndex{}, ErrVersionConflict
		}
	}
}
