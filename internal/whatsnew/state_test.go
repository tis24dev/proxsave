package whatsnew

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestMarkSeenRoundTrip: MarkSeen then LoadState returns the persisted version with
// present=true and a nil error.
func TestMarkSeenRoundTrip(t *testing.T) {
	base := t.TempDir()
	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	st, present, err := LoadState(base)
	if err != nil {
		t.Fatalf("LoadState: unexpected error %v", err)
	}
	if !present {
		t.Fatalf("LoadState present = false, want true after MarkSeen")
	}
	if st.LastSeenNotesVersion != "0.30.0" {
		t.Fatalf("LastSeenNotesVersion = %q, want %q", st.LastSeenNotesVersion, "0.30.0")
	}
}

// TestMarkSeenMkdirAllAndModes: on a base with no identity/ dir, MarkSeen creates it
// and writes the file at StatePath; the file mode is 0600 and the dir mode is 0750.
func TestMarkSeenMkdirAllAndModes(t *testing.T) {
	base := t.TempDir()
	// Sanity: identity/ must not exist yet.
	if _, err := os.Stat(filepath.Dir(StatePath(base))); !os.IsNotExist(err) {
		t.Fatalf("identity/ should not exist before MarkSeen, stat err = %v", err)
	}
	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}

	fi, err := os.Stat(StatePath(base))
	if err != nil {
		t.Fatalf("stat flag file: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Fatalf("flag file mode = %o, want 0600", got)
	}
	di, err := os.Stat(filepath.Dir(StatePath(base)))
	if err != nil {
		t.Fatalf("stat identity dir: %v", err)
	}
	if got := di.Mode().Perm(); got != 0o750 {
		t.Fatalf("identity dir mode = %o, want 0750", got)
	}
}

// TestLoadStateMissingFile: an untouched base has no file -> zero State, present=false,
// nil error.
func TestLoadStateMissingFile(t *testing.T) {
	base := t.TempDir()
	st, present, err := LoadState(base)
	if err != nil {
		t.Fatalf("LoadState on missing file: unexpected error %v", err)
	}
	if present {
		t.Fatalf("present = true on missing file, want false")
	}
	if st.LastSeenNotesVersion != "" {
		t.Fatalf("State should be zero on missing file, got %+v", st)
	}
}

// TestLoadStateEmptyFile: a zero-byte flag file is treated exactly like a missing file.
func TestLoadStateEmptyFile(t *testing.T) {
	base := t.TempDir()
	path := StatePath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write empty flag file: %v", err)
	}
	st, present, err := LoadState(base)
	if err != nil {
		t.Fatalf("LoadState on empty file: unexpected error %v", err)
	}
	if present {
		t.Fatalf("present = true on empty file, want false")
	}
	if st.LastSeenNotesVersion != "" {
		t.Fatalf("State should be zero on empty file, got %+v", st)
	}
}

// TestLoadStateMalformedJSON: garbage content surfaces as an ErrStateParse error, and
// the returned State is the zero value with present=false.
func TestLoadStateMalformedJSON(t *testing.T) {
	base := t.TempDir()
	path := StatePath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write bad flag file: %v", err)
	}
	st, present, err := LoadState(base)
	if err == nil {
		t.Fatalf("LoadState on bad JSON should error")
	}
	if !errors.Is(err, ErrStateParse) {
		t.Fatalf("error = %v, want errors.Is ErrStateParse", err)
	}
	if present {
		t.Fatalf("present = true on bad JSON, want false")
	}
	if st.LastSeenNotesVersion != "" {
		t.Fatalf("State should be zero on bad JSON, got %+v", st)
	}
}

// TestLoadStateNonSemverVersion: a syntactically valid JSON flag whose stored version is
// not valid semver (a garbage string, or an empty version via a bare {}) is treated as
// corrupt -- ErrStateParse, present=false, zero State -- so a writer self-heals it instead
// of the feature silencing forever. MarkSeen only ever writes a non-empty semver, so this
// never rejects a flag the app itself produced.
func TestLoadStateNonSemverVersion(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"garbage version", `{"last_seen_notes_version":"garbage"}`},
		{"empty version object", `{}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			path := StatePath(base)
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatalf("mkdir identity dir: %v", err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write flag: %v", err)
			}
			st, present, err := LoadState(base)
			if err == nil {
				t.Fatalf("LoadState on %s should error", tc.name)
			}
			if !errors.Is(err, ErrStateParse) {
				t.Fatalf("error = %v, want errors.Is ErrStateParse", err)
			}
			if present {
				t.Fatalf("present = true on %s, want false", tc.name)
			}
			if st.LastSeenNotesVersion != "" {
				t.Fatalf("State should be zero on %s, got %+v", tc.name, st)
			}
		})
	}
}

// TestMarkSeenSelfHealsCorruptFile (STATE-06 self-heal SEAM): pre-write garbage at
// StatePath, then MarkSeen succeeds, quarantines the garbage to StatePath+".corrupt",
// and the primary file is valid JSON holding the new version.
func TestMarkSeenSelfHealsCorruptFile(t *testing.T) {
	base := t.TempDir()
	path := StatePath(base)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir identity dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("{garbage bytes"), 0o600); err != nil {
		t.Fatalf("write garbage flag file: %v", err)
	}

	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen over corrupt file: %v", err)
	}

	// Garbage was quarantined.
	quarantine := path + ".corrupt"
	if _, err := os.Stat(quarantine); err != nil {
		t.Fatalf("corrupt bytes should be quarantined to %s, stat err = %v", quarantine, err)
	}
	qb, err := os.ReadFile(quarantine)
	if err != nil {
		t.Fatalf("read quarantine: %v", err)
	}
	if string(qb) != "{garbage bytes" {
		t.Fatalf("quarantine content = %q, want the original garbage", string(qb))
	}

	// Primary file is valid JSON holding the new version.
	pb, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read primary flag file: %v", err)
	}
	var st State
	if err := json.Unmarshal(pb, &st); err != nil {
		t.Fatalf("primary file should be valid JSON, got %q (err %v)", string(pb), err)
	}
	if st.LastSeenNotesVersion != "0.30.0" {
		t.Fatalf("primary version = %q, want %q", st.LastSeenNotesVersion, "0.30.0")
	}

	// And LoadState now reads it cleanly.
	got, present, err := LoadState(base)
	if err != nil || !present || got.LastSeenNotesVersion != "0.30.0" {
		t.Fatalf("post-heal LoadState = (%+v, %v, %v), want the new version present", got, present, err)
	}
}

// TestStatePathShape pins the file location the installer seed and dashboard read both
// depend on.
func TestStatePathShape(t *testing.T) {
	base := "/opt/proxsave"
	want := filepath.Join(base, "identity", ".whatsnew_seen.json")
	if got := StatePath(base); got != want {
		t.Fatalf("StatePath = %q, want %q", got, want)
	}
}

// TestMarkSeenLeavesNoTmp: the write goes through a ".tmp" sibling renamed into place,
// so the identity dir must hold no leftover temp afterwards.
func TestMarkSeenLeavesNoTmp(t *testing.T) {
	base := t.TempDir()
	if err := MarkSeen(base, "0.30.0"); err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	dir := filepath.Dir(StatePath(base))
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read identity dir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover temp file after atomic write: %s", e.Name())
		}
	}
}
