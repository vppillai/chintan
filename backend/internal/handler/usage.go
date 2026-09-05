package handler

import (
	"math/big"
	"net/http"
	"time"

	"github.com/vppillai/chintan/backend/internal/httperr"
	"github.com/vppillai/chintan/backend/internal/middleware"
	"github.com/vppillai/chintan/backend/internal/service"
	"github.com/vppillai/chintan/backend/internal/usage"
)

// Usage is the OpenAPI Usage schema: one tenant's provider usage for one
// month, the API requests they made, what their recordings and notes occupy,
// and beside it the instance's AWS cost for the same month.
//
// The provider half is the storage row's own counters, which is the exception
// to wire.go's rule and a deliberate one: the row holds nothing but numbers the
// caller is entitled to see, and there is no S3 key or internal id on it to
// leak. The other members are why this is a wire type rather than an alias of
// usage.Month: they come from other rows — the instance partition, the
// tenant's capture and note indexes — and are only joined here.
type Usage struct {
	Month string `json:"month"`
	usage.Totals
	Ops map[string]usage.Totals `json:"ops"`
	// Providers is the same totals split by provider name (groq, openai).
	// A provider that made no call in the month is absent; never null.
	Providers map[string]usage.Totals `json:"providers"`
	Days      []usage.Day             `json:"days"`
	// API counts the authenticated requests this caller made in the month.
	API UsageAPI `json:"api"`
	// Storage is what the caller's recordings and notes add up to, computed
	// from the index rows at read time.
	Storage UsageStorage `json:"storage"`
	// AWS is the instance's AWS spend for the month as last read from the
	// stack's budget by the worker's daily task, or null when nothing has been
	// recorded for the month: the stack has no budget, or the task has not run
	// since the month began. The figure is instance-level; share_micros is
	// this caller's slice of it.
	AWS *UsageAWS `json:"aws"`
}

// UsageAPI is the OpenAPI UsageApi schema.
type UsageAPI struct {
	Requests int64 `json:"requests"`
}

// UsageStorage is the OpenAPI UsageStorage schema.
type UsageStorage struct {
	Recordings   int     `json:"recordings"`
	AudioSeconds float64 `json:"audio_seconds"`
	AudioBytes   int64   `json:"audio_bytes"`
	Notes        int     `json:"notes"`
	// Approximate is true when the walk behind the numbers stopped at its
	// row cap, so they are a floor rather than the total.
	Approximate bool `json:"approximate"`
}

// UsageAWS is the OpenAPI UsageAws schema: the instance figure, plus the
// caller's share of it.
type UsageAWS struct {
	usage.AWSCost
	// ShareMicros is the instance's AWS cost multiplied by this caller's
	// share of the instance's provider spend for the month, or null when the
	// instance spent nothing at the providers — there is then nothing to
	// apportion by.
	ShareMicros *int64 `json:"share_micros"`
	// ShareBasis names how ShareMicros was apportioned: "provider_cost"
	// today; null when ShareMicros is null.
	ShareBasis *string `json:"share_basis"`
}

// shareBasisProviderCost is the one apportionment rule there is: a tenant that
// caused 40% of the month's provider spend caused, roughly, 40% of the Lambda
// seconds, DynamoDB writes and S3 puts behind it. It is a policy, and it is
// named on the wire so a different one can be told apart later.
const shareBasisProviderCost = "provider_cost"

// getUsage answers the caller's own usage for one month. It is what a "You"
// screen shows; it deliberately carries no per-capture detail — the month, its
// splits and one line per day is small enough to fetch on every visit and is
// all a person can act on.
func (rt *router) getUsage(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.GetUserID(r.Context())
	if !ok {
		httperr.Unauthorized(w, r, "authentication required")
		return
	}
	if rt.Usage == nil {
		httperr.ServiceUnavailable(w, r, "usage accounting is not configured on this instance")
		return
	}

	month := r.URL.Query().Get("month")
	if month == "" {
		// UTC, the same calendar the worker bills to. A user west of Greenwich
		// asking on the evening of the 31st is shown the month the calls were
		// counted in, which is the one the numbers add up in.
		month = time.Now().UTC().Format("2006-01")
	}
	if !usage.ValidMonth(month) {
		httperr.BadRequest(w, r, "month must be yyyy-mm")
		return
	}

	got, err := rt.Usage.Month(r.Context(), userID, month)
	if err != nil {
		fail(w, r, err)
		return
	}
	awsCost, err := rt.Usage.AWSCost(r.Context(), month)
	if err != nil {
		fail(w, r, err)
		return
	}
	var aws *UsageAWS
	if awsCost != nil {
		instance, err := rt.Usage.InstanceSpend(r.Context(), month)
		if err != nil {
			fail(w, r, err)
			return
		}
		aws = &UsageAWS{AWSCost: *awsCost}
		if share, ok := shareOf(awsCost.MonthMicros, got.CostMicros, instance); ok {
			basis := shareBasisProviderCost
			aws.ShareMicros, aws.ShareBasis = &share, &basis
		}
	}

	var storage service.StorageSummary
	if rt.Storage != nil {
		storage, err = rt.Storage.Summarize(r.Context(), userID)
		if err != nil {
			fail(w, r, err)
			return
		}
	}

	if got.Providers == nil {
		got.Providers = map[string]usage.Totals{}
	}
	writeJSON(w, http.StatusOK, Usage{
		Month:     got.Month,
		Totals:    got.Totals,
		Ops:       got.Ops,
		Providers: got.Providers,
		Days:      got.Days,
		API:       UsageAPI{Requests: got.APIRequests},
		Storage: UsageStorage{
			Recordings:   storage.Recordings,
			AudioSeconds: storage.AudioSeconds,
			AudioBytes:   storage.AudioBytes,
			Notes:        storage.Notes,
			Approximate:  storage.Approximate,
		},
		AWS: aws,
	})
}

// shareOf apportions the instance's AWS cost to a tenant by its fraction of
// the instance's provider spend: aws × tenant ÷ instance, rounded to the
// nearest microdollar. ok is false when the instance spent nothing, since a
// share of zero is not a fraction. Exact integer arithmetic, because the
// product of two month totals in microdollars can pass int64.
func shareOf(awsMicros, tenantMicros, instanceMicros int64) (int64, bool) {
	if instanceMicros <= 0 || tenantMicros < 0 || awsMicros < 0 {
		return 0, false
	}
	num := new(big.Int).Mul(big.NewInt(awsMicros), big.NewInt(tenantMicros))
	den := big.NewInt(instanceMicros)
	// Round half up: (2·num + den) ÷ (2·den).
	num.Mul(num, big.NewInt(2)).Add(num, den)
	den.Mul(den, big.NewInt(2))
	return num.Quo(num, den).Int64(), true
}
