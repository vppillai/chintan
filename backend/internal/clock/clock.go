// Package clock supplies the current time through an injectable seam.
//
// Nothing in this codebase calls time.Now() directly outside this package. Three
// things depend on that:
//
//   - **Metering and audit records are timestamped and write-once** (I12, I13). A test
//     that cannot control the clock cannot assert on the key a record lands under, and
//     the key includes a month (§6.3).
//   - **The daily spend breaker sums a day's usage** (§10.5.9). "Today" has to be
//     something a test can move.
//   - **TTL values are absolute epoch seconds.** Asserting that a usage record expires
//     in 25 months and an audit record in 7 years requires a fixed now.
//
// Build-time values are deliberately *not* sourced here: version.BuildTime is injected
// at link time, so nothing at runtime depends on the build host's clock (§0.6).
package clock

import "time"

// Clock returns the current time. Always UTC.
type Clock interface {
	Now() time.Time
}

// System is the real clock.
type System struct{}

// Now returns the current UTC time.
//
// UTC, not local. Every timestamp that reaches a key must sort lexicographically in
// the same order it sorts chronologically, and that only holds in one zone (see
// keys.GSI1, which rejects a non-UTC timestamp outright).
func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a clock stopped at an instant, for tests.
type Fixed struct{ T time.Time }

// Now returns the fixed instant, in UTC.
func (f Fixed) Now() time.Time { return f.T.UTC() }

// Advance moves a Fixed clock forward. Returns a new value rather than mutating, so a
// test that shares a clock between components cannot have it moved underneath it by
// accident.
func (f Fixed) Advance(d time.Duration) Fixed { return Fixed{T: f.T.Add(d)} }

// RFC3339UTC formats a time in the one representation this system stores.
//
// Second precision, Z suffix. Sub-second precision is dropped deliberately: it appears
// in sort keys, and two records written in the same second must collide predictably
// rather than depending on how fast the machine was — ULID ordering is what
// disambiguates them (§6.3).
func RFC3339UTC(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// Month formats a time as yyyy-mm, the partition for usage records (§6.3).
func Month(t time.Time) string { return t.UTC().Format("2006-01") }

// Date formats a time as yyyy-mm-dd, the partition for metric records and the window
// the spend breaker sums over.
func Date(t time.Time) string { return t.UTC().Format("2006-01-02") }
