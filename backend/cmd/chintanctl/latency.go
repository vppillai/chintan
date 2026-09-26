package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/vppillai/chintan/backend/internal/model"
)

// latencyHops is the print order of the hops. Each is milliseconds between
// two stamps on the capture row — the sender's recorded_at, the row's
// created_at, the pipeline's stage_at entries and appended_at
// (model.CaptureIndex) — so the command reads what the worker already wrote
// and nothing is measured twice.
var latencyHops = []string{"device_lag", "queue", "transcribe", "route", "clean", "append", "total"}

// latencyStat is one hop's distribution over a group.
type latencyStat struct {
	Count int   `json:"count"`
	P50   int64 `json:"p50_ms"`
	P95   int64 `json:"p95_ms"`
	Max   int64 `json:"max_ms"`
}

// latencyGroup is every capture of one source in the month: `app`, or
// `device:<id>` — the id is fine in the operator's terminal, where the
// worker's metric keeps to `device`.
type latencyGroup struct {
	Source   string `json:"source"`
	Captures int    `json:"captures"`
	// Outcomes counts the captures that did not reach appended, by status.
	Outcomes map[string]int `json:"outcomes"`
	// Legacy counts rows from before the timing record (2026-09-26), which
	// have appended_at and nothing per stage, so they contribute total only.
	Legacy int                    `json:"legacy"`
	Hops   map[string]latencyStat `json:"hops"`

	samples map[string][]int64
}

type latencyResult struct {
	Target target         `json:"target"`
	Month  string         `json:"month"`
	Groups []latencyGroup `json:"groups"`
}

func (r *latencyResult) human(w *lineWriter) {
	w.printf("latency %s (%s) for %s\n", r.Target.Instance, r.Target.Environment, r.Month)
	if len(r.Groups) == 0 {
		w.printf("  no capture was created in this month\n")
		return
	}
	for _, g := range r.Groups {
		outcomes := make([]string, 0, len(g.Outcomes))
		for status, n := range g.Outcomes {
			outcomes = append(outcomes, fmt.Sprintf("%s %d", status, n))
		}
		sort.Strings(outcomes)
		appended := g.Captures
		for _, n := range g.Outcomes {
			appended -= n
		}
		line := fmt.Sprintf("  %s: %d captures, %d appended", g.Source, g.Captures, appended)
		if len(outcomes) > 0 {
			line += ", " + strings.Join(outcomes, ", ")
		}
		if g.Legacy > 0 {
			line += fmt.Sprintf(", %d legacy (total only)", g.Legacy)
		}
		w.printf("%s\n", line)
		w.printf("    %-12s %6s %9s %9s %9s\n", "hop", "count", "p50 ms", "p95 ms", "max ms")
		for _, hop := range latencyHops {
			s, ok := g.Hops[hop]
			if !ok {
				continue
			}
			w.printf("    %-12s %6d %9d %9d %9d\n", hop, s.Count, s.P50, s.P95, s.Max)
		}
	}
}

func cmdLatency(ctx context.Context, args []string, stdout, stderr io.Writer, stdin io.Reader) error {
	var g globalFlags
	var month string
	fs := newFlagSet("latency", stderr)
	g.register(fs, true, false)
	fs.StringVar(&month, "month", time.Now().UTC().Format("2006-01"), "calendar month, UTC, as yyyy-mm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !usageMonthRe.MatchString(month) {
		return fmt.Errorf("--month %q must be yyyy-mm", month)
	}
	e, err := dial(ctx, g, stdout, stderr, stdin)
	if err != nil {
		return err
	}
	res, err := runLatency(ctx, e, month, g.tenants)
	if err != nil {
		return err
	}
	return report(stdout, g.jsonOut, res)
}

// runLatency walks each tenant's CAPTURE# rows — the named tenants, else
// every tenant the bucket knows, as export and backup find them — keeps the
// month's, and reduces each row to its hops. Percentiles are nearest-rank
// over the sorted samples. It writes nothing.
func runLatency(ctx context.Context, e *env, month string, explicitTenants []string) (*latencyResult, error) {
	tenants, err := resolveTenants(ctx, e.Blobs, explicitTenants)
	if err != nil {
		return nil, err
	}
	groups := map[string]*latencyGroup{}
	for _, id := range tenants {
		err := e.Part.Scan(ctx, tenantPK(id), "CAPTURE#", func(it Item) error {
			c, err := captureFromItem(it)
			if err != nil {
				return err
			}
			// created_at is RFC 3339 in UTC, so the month is its prefix.
			if !strings.HasPrefix(c.CreatedAt, month+"-") {
				return nil
			}
			source := c.Source
			if source == "" {
				source = "app"
			}
			g := groups[source]
			if g == nil {
				g = &latencyGroup{Source: source, Outcomes: map[string]int{}, Hops: map[string]latencyStat{}, samples: map[string][]int64{}}
				groups[source] = g
			}
			g.Captures++
			if c.Status != model.StatusAppended {
				g.Outcomes[string(c.Status)]++
			}
			hops, legacy := latencyHopsOf(c)
			if legacy {
				g.Legacy++
			}
			for hop, ms := range hops {
				g.samples[hop] = append(g.samples[hop], ms)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	res := &latencyResult{Target: e.Target, Month: month, Groups: []latencyGroup{}}
	for _, g := range groups {
		for hop, samples := range g.samples {
			sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
			g.Hops[hop] = latencyStat{
				Count: len(samples),
				P50:   percentile(samples, 50),
				P95:   percentile(samples, 95),
				Max:   samples[len(samples)-1],
			}
		}
		res.Groups = append(res.Groups, *g)
	}
	sort.Slice(res.Groups, func(i, j int) bool { return res.Groups[i].Source < res.Groups[j].Source })
	return res, nil
}

// latencyHopsOf reduces one row to its hops in milliseconds. A hop whose two
// stamps are not both on the row is left out rather than guessed: a text
// capture has no transcribe hop, a targeted one no route hop, a failed one
// no total. A row with no stage_at at all is from before the timing record
// and gives total alone, from appended_at.
func latencyHopsOf(c model.CaptureIndex) (hops map[string]int64, legacy bool) {
	created, err := model.ParseTime(c.CreatedAt)
	if err != nil {
		return nil, false
	}
	hops = map[string]int64{}
	between := func(from, to time.Time) int64 { return to.Sub(from).Milliseconds() }
	if recorded, err := model.ParseTime(c.RecordedAt); err == nil {
		hops["device_lag"] = between(recorded, created)
	}
	at := func(status model.CaptureStatus) (time.Time, bool) {
		t, err := model.ParseTime(c.StageAt[string(status)])
		return t, err == nil
	}
	appended, hasAppended := at(model.StatusAppended)
	if !hasAppended && c.AppendedAt != 0 {
		appended, hasAppended = time.Unix(c.AppendedAt, 0), true
	}
	if len(c.StageAt) == 0 {
		if hasAppended {
			hops["total"] = between(created, appended)
		}
		return hops, true
	}
	var first time.Time
	for _, v := range c.StageAt {
		if t, err := model.ParseTime(v); err == nil && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	if !first.IsZero() {
		hops["queue"] = between(created, first)
	}
	span := func(hop string, from, to model.CaptureStatus) {
		a, okA := at(from)
		b, okB := at(to)
		if okA && okB {
			hops[hop] = between(a, b)
		}
	}
	span("transcribe", model.StatusTranscribing, model.StatusTranscribed)
	span("route", model.StatusRouting, model.StatusCleaning)
	span("clean", model.StatusCleaning, model.StatusAppending)
	if appending, ok := at(model.StatusAppending); ok && hasAppended {
		hops["append"] = between(appending, appended)
	}
	if hasAppended {
		hops["total"] = between(created, appended)
	}
	return hops, false
}

// percentile is the nearest-rank percentile of sorted, which must not be
// empty.
func percentile(sorted []int64, p float64) int64 {
	i := int(math.Ceil(p/100*float64(len(sorted)))) - 1
	if i < 0 {
		i = 0
	}
	return sorted[i]
}
