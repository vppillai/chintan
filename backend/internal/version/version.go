// Package version reports what build this is.
//
// §0.6: the running version is visible in the app and derived from git tags at
// build time. Three rules from that section are implemented here, each of which
// exists because passbook got it wrong first:
//
//   - No checked-in VERSION file. Passbook carried one, it had to be hand-synced
//     with tags, and it drifted — still reading v2.6.0 at the v2.7.0 release
//     before being removed (G-037). A tag cannot drift from itself, so the value
//     is injected from `git describe` at link time.
//
//   - The service worker cache token is {tag}-{short-sha}, not the bare tag.
//     Cache identity must track *content*: a deploy without a fresh tag produces
//     a byte-identical sw.js, the browser detects no worker update, and an
//     installed PWA serves new markup against old JavaScript indefinitely with
//     no way for the user to clear it (G-035).
//
//   - The API exposes its own version, because the frontend and backend deploy
//     through separate workflows and can drift. The app displays both and flags a
//     mismatch — a class of bug otherwise diagnosed by guesswork.
//
// Tag before deploying, not after: CI resolves `git describe` during the build,
// so a tag pushed afterwards does not reach the artifact (G-036).
package version

import "strings"

// Injected at link time by scripts/build-lambda.sh via -ldflags. The defaults
// are what an unstamped local build reports; they are deliberately not a
// plausible version string, so an unstamped artifact is obvious rather than
// looking like release 0.0.0.
var (
	// Tag is `git describe --tags --abbrev=0`, e.g. "v0.1.0".
	Tag = "unstamped"
	// Commit is the short SHA of the build commit.
	Commit = "unknown"
	// BuildTime is an RFC3339 UTC timestamp, supplied by the build so that
	// nothing in this package reads a clock.
	BuildTime = "unknown"
)

// Display returns the version to show a human. The bare tag, per §0.6 — the
// cache token is a separate concern and must not leak into display.
func Display() string { return Tag }

// CacheToken returns the service worker cache identity: {tag}-{short-sha}.
//
// This is the whole of G-035's fix. It must change on every deploy rather than
// every tag, which is why the SHA is in it. Never substitute Display() here.
func CacheToken() string { return Tag + "-" + Commit }

// Stamped reports whether this build carries real version information. The
// health endpoint surfaces it so an accidentally unstamped deploy is visible
// rather than being read as version "unstamped" and ignored.
func Stamped() bool {
	return Tag != "unstamped" && Commit != "unknown" && !strings.Contains(Tag, "$")
}
