package whatsnew

import (
	"strings"

	"github.com/Masterminds/semver/v3"
)

// IsDevBuild is the dev/pseudo-version guard: a dev build or a Go pseudo-version must
// never gate the what's-new screen (a developer running an untagged or dirty binary is
// not a released upgrade). STATE-05 is enforced here. Returns true for an empty version,
// the "0.0.0-dev" placeholder (with or without a leading v), any value whose v-stripped
// form has the "0.0.0-" prefix (the Go pseudo-version heuristic v0.0.0-<timestamp>-<hash>),
// any build carrying semver metadata (the make-build "X.Y.Z-dev.N+gSHA" or a dirty
// "X.Y.Z+gSHA"), or any build whose prerelease's first dot-identifier is "dev" (e.g.
// "X.Y.Z-dev" or "X.Y.Z-dev.N"). A non-semver string is NOT classified dev here: IsUnseen
// already fails toward silence on an unparseable current. Clean releases (0.30.0, 1.0.0)
// and legit beta/rc prereleases (0.30.0-beta5, 0.30.0-rc1) carry no metadata and no "dev"
// prerelease, so they still gate.
func IsDevBuild(current string) bool {
	c := strings.TrimSpace(current)
	if c == "" || c == "0.0.0-dev" || c == "v0.0.0-dev" {
		return true
	}
	stripped := strings.TrimPrefix(c, "v")
	if strings.HasPrefix(stripped, "0.0.0-") {
		return true // Go pseudo-version heuristic (v0.0.0-<timestamp>-<hash>)
	}
	v, err := semver.NewVersion(stripped)
	if err != nil {
		return false // non-semver is NOT dev; IsUnseen fails toward silence on it
	}
	if v.Metadata() != "" {
		return true // build metadata (+gSHA / dirty) marks an untagged or dirty build
	}
	if pre := v.Prerelease(); pre != "" {
		first := pre
		if i := strings.IndexByte(pre, '.'); i >= 0 {
			first = pre[:i]
		}
		if first == "dev" {
			return true // "X.Y.Z-dev" or "X.Y.Z-dev.N" development prerelease
		}
	}
	return false
}

// finalize returns v with its prerelease and build metadata stripped, so every build of an
// X.Y.Z line (0.30.0-beta6, 0.30.0+meta, 0.30.0) collapses to the same finalized X.Y.Z. The
// what's-new feature keys notes to FINAL releases, so a beta must see (and acknowledge) its
// line's notes exactly once across the whole line, not empty on the beta then again on the final.
func finalize(v *semver.Version) *semver.Version {
	return semver.New(v.Major(), v.Minor(), v.Patch(), "", "")
}

// IsUnseen reports whether the installed version carries notes the user has not
// acknowledged. It fails toward SILENCE: an unparseable current version returns
// (false, err), never (true, _). An absent flag (present=false) is "unseen" (STATE-04,
// the real-upgrader case). A parse error on lastSeen while the flag is present also
// returns (false, err) so a corrupt flag stays silent. The comparison is on the FINALIZED
// versions (prerelease/metadata stripped), so a user who acknowledged a line on any beta is
// not re-shown on the final: 0.30.0-beta6 and 0.30.0 are the same notes line.
// Masterminds/semver tolerates a missing "v" on either side.
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
		return false, err // corrupt last-seen: fail toward silence
	}
	return finalize(cur).GreaterThan(finalize(last)), nil
}

// Decide is the single dashboard entry point: it loads the flag, applies the dev-build
// and semver gates, and (only when unseen) composes the notes body. It returns
// (show, body, err). Every error path and every not-unseen path returns show=false with
// an empty body, so the feature can only ever fail toward silence, never toward
// "show everything". The notes range is (from, current], which LookupNotes finalizes (strips
// prerelease/metadata) so a prerelease build sees its final line's notes (a beta of 0.30.0
// gets the 0.30.0 entry). RenderBody's header still shows the RAW running build, so a beta
// honestly reads "ProxSave 0.30.0-beta6" above the 0.30.0 notes.
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

// ShouldWarn is the non-interactive counterpart of Decide, used by the run-log nudge on
// automated/non-interactive runs. It shares the exact IsDevBuild + LoadState + IsUnseen
// core as Decide (minus LookupNotes/RenderBody): instead of composing a notes body it
// returns the normalized (v-stripped) current version for the caller's warning copy. Like
// Decide it fails toward SILENCE: every error path and every not-unseen path returns
// show=false with an empty version, so the nudge can only ever go quiet, never show-all.
func ShouldWarn(baseDir, current string) (show bool, version string, err error) {
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
	return true, strings.TrimPrefix(strings.TrimSpace(current), "v"), nil
}
