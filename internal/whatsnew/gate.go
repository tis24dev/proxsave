package whatsnew

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

// IsDevBuild is the dev/pseudo-version guard SEAM. A dev build or a Go pseudo-version
// must never gate the what's-new screen (a developer running an untagged binary is not
// a released upgrade). Phase 1 establishes only the call seam and a smoke test; the full
// STATE-05 enforcement plus its dedicated matrix is Phase 3. Returns true for an empty
// version, the "0.0.0-dev" placeholder (with or without a leading v), or any value whose
// v-stripped form has the "0.0.0-" prefix (the Go pseudo-version heuristic
// v0.0.0-<timestamp>-<hash>).
func IsDevBuild(current string) bool {
	c := strings.TrimSpace(current)
	if c == "" || c == "0.0.0-dev" || c == "v0.0.0-dev" {
		return true
	}
	return strings.HasPrefix(strings.TrimPrefix(c, "v"), "0.0.0-")
}

// IsUnseen reports whether the installed version carries notes the user has not
// acknowledged. It fails toward SILENCE: an unparseable current version returns
// (false, err), never (true, _). An absent flag (present=false) is "unseen" (STATE-04,
// the real-upgrader case). A parse error on lastSeen while the flag is present also
// returns (false, err) so a corrupt flag stays silent until Phase 3 self-heals it.
// Masterminds/semver tolerates a missing "v" on either side and orders prereleases
// correctly (0.30.0-beta5 < 0.30.0).
func IsUnseen(current, lastSeen string, present bool) (bool, error) {
	cur, err := semver.NewVersion(current)
	if err != nil {
		return false, err // fail toward silence
	}
	if !present {
		return true, nil // absent flag on a non-fresh host = unseen (STATE-04)
	}
	last, err := semver.NewVersion(lastSeen)
	if err != nil {
		return false, err // corrupt last-seen: fail toward silence (Phase 3 self-heals)
	}
	return cur.GreaterThan(last), nil
}

// Decide is the single dashboard entry point: it loads the flag, applies the dev-build
// and semver gates, and (only when unseen) composes the notes body. It returns
// (show, body, err). Every error path and every not-unseen path returns show=false with
// an empty body, so the feature can only ever fail toward silence, never toward
// "show everything". The notes range is (from, current] where from is "0.0.0" for an
// absent flag (a real upgrader catches up from the beginning) or the last-seen version
// otherwise.
func Decide(baseDir, current string) (show bool, body string, err error) {
	if IsDevBuild(current) {
		return false, "", nil
	}
	state, present, err := LoadState(baseDir)
	if err != nil {
		return false, "", err
	}
	unseen, err := IsUnseen(current, state.LastSeenNotesVersion, present)
	if err != nil || !unseen {
		return false, "", err
	}
	from := "0.0.0"
	if present {
		from = state.LastSeenNotesVersion
	}
	return true, RenderBody(current, LookupNotes(from, current)), nil
}
