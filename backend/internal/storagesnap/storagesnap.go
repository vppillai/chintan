// Package storagesnap measures what each tenant is storing, once a day, and
// adds the reading onto the tenant's usage rows, so that storage has a figure
// over time and not only a figure right now.
//
// GET /v1/usage answers `storage` from the index rows when the request is
// served. That is the footprint at that moment: a tenant who deletes every
// note on the 30th reads zero on the 31st, though the bucket held their audio
// for the whole month and S3 billed the account for it. Storage is priced in
// byte-months, so the honest per-tenant figure is a sum of daily readings —
// bytes held today, added to the month — and that is what this task takes:
// byte-days (`storage_byte_days`) and note-days (`storage_note_days`) on the
// tenant's USAGE#<yyyy-mm> row, and the day's own reading on USAGE#<yyyy-mm-dd>.
// Nothing here prices anything; the client turns byte-days into an estimate
// with a price it names.
//
// An EventBridge rule invokes the worker daily with {"task":"storage-snapshot"};
// the worker dispatches to Snapshotter.Run. Which tenants exist is the one hard
// question: a single-table design with no Scan grant cannot list partition
// keys. The task walks the tenants that have a USAGE#<month> row for this
// month or any of the twelve before — the GSI1 query the admin listing uses.
// Every authenticated API request writes that row, and so does this task, so
// once a tenant has been snapshotted they stay in the set for as long as the
// month rows are kept, which is for good. The gap is a tenant with storage and
// no usage row in thirteen months, who is not counted until they touch the
// app; docs/design/usage-accounting.md records it.
package storagesnap

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/vppillai/chintan/backend/internal/obs"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Task is the payload field value the EventBridge rule sends, and cmd/worker
// dispatches on. The template's rule and this constant must agree.
const Task = "storage-snapshot"

// lookbackMonths is how many months of USAGE# rows, the current one included,
// name the tenants to snapshot. Thirteen is the day rows' retention and covers
// a tenant who used the app once this time last year and kept their notes.
const lookbackMonths = 13

// Summarizer measures one tenant's footprint. service.StorageService is the
// one implementation; the interface is what the tests stand in for.
type Summarizer interface {
	Summarize(ctx context.Context, userID string) (service.StorageSummary, error)
}

// Store names the tenants and takes the readings. usage.Dynamo is both.
type Store interface {
	usage.TenantLister
	usage.StorageDayStore
}

// Snapshotter runs one storage-snapshot task.
type Snapshotter struct {
	store   Store
	storage Summarizer
	now     func() time.Time
}

// New builds the task. Both dependencies are required: a snapshot with no
// store to write to or nothing to measure is a misconfiguration to fail at
// init, where the deploy sees it.
func New(store Store, storage Summarizer) (*Snapshotter, error) {
	switch {
	case store == nil:
		return nil, errors.New("storagesnap: store is required")
	case storage == nil:
		return nil, errors.New("storagesnap: storage service is required")
	}
	return &Snapshotter{store: store, storage: storage, now: time.Now}, nil
}

// Result is what one run did, for the log and the tests.
type Result struct {
	Day string
	// Tenants is how many the month rows named.
	Tenants int
	// Added is how many took today's reading; Skipped is how many already had
	// it (a retry, or a second invocation the same day); Empty is how many
	// had nothing stored and were left alone.
	Added, Skipped, Empty int
}

// Run takes today's reading for every tenant the usage rows name.
//
// One tenant's failure does not stop the others: the loop finishes, and the
// first error is returned so Lambda retries the task — which is safe, because
// AddStorageDay adds nothing to a month that already has today's reading. A
// tenant with nothing stored is skipped rather than written as zeros: the
// zeros would keep a departed tenant in the month rows for ever, and there is
// nothing to bill.
func (s *Snapshotter) Run(ctx context.Context) (Result, error) {
	now := s.now().UTC()
	res := Result{Day: now.Format("2006-01-02")}

	tenants, err := s.store.TenantsWithUsage(ctx, monthsBack(now, lookbackMonths))
	if err != nil {
		return res, err
	}
	res.Tenants = len(tenants)

	var first error
	for _, tenantID := range tenants {
		tctx := obs.WithTenant(ctx, tenantID)
		summary, err := s.storage.Summarize(tctx, tenantID)
		if err != nil {
			obs.Log(tctx).Error("storage-snapshot: could not measure the tenant", slog.String("error", err.Error()))
			first = errors.Join(first, err)
			continue
		}
		if summary.AudioBytes == 0 && summary.Notes == 0 && summary.Recordings == 0 {
			res.Empty++
			continue
		}
		added, err := s.store.AddStorageDay(tctx, usage.StorageSnapshot{
			TenantID: tenantID, Day: res.Day, AudioBytes: summary.AudioBytes, Notes: int64(summary.Notes),
		})
		if err != nil {
			obs.Log(tctx).Error("storage-snapshot: could not record the reading", slog.String("error", err.Error()))
			first = errors.Join(first, err)
			continue
		}
		if !added {
			res.Skipped++
			continue
		}
		res.Added++
		obs.Log(tctx).Info("storage-snapshot: recorded the day",
			slog.String("day", res.Day),
			slog.Int64("audio_bytes", summary.AudioBytes),
			slog.Int("notes", summary.Notes),
			slog.Bool("approximate", summary.Approximate))
	}

	obs.Log(ctx).Info("storage-snapshot: finished",
		slog.String("day", res.Day),
		slog.Int("tenants", res.Tenants),
		slog.Int("added", res.Added),
		slog.Int("skipped", res.Skipped),
		slog.Int("empty", res.Empty))
	return res, first
}

// monthsBack is the yyyy-mm of now's month and the n-1 before it, newest
// first. Computed from the first of the month so the 31st does not skip
// February.
func monthsBack(now time.Time, n int) []string {
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, first.AddDate(0, -i, 0).Format("2006-01"))
	}
	return out
}
