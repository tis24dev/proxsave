package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Characterization lock for the six PBS section-config applies that restore a staged
// .cfg through proxmox-backup-manager: remote, s3 endpoint, sync-job, verify-job,
// prune-job and traffic-control. Written before they are folded into one shared helper,
// so that refactor is judged against tests that predate it.
//
// Every case runs the real apply function against a staged file, a command runner that
// answers by verb and object id, and a logger that records every level, then freezes
// one block of a per-kind golden file: the staged input, the runner's scripted answers,
// the files read, every command in order with its exact argument boundaries, the
// returned error (text, wrapping chain and sentinel identity) and every log line.
//
// On top of the goldens, each case checks what a golden cannot say on its own: that no
// secret of remote.cfg or s3.cfg reaches an error or a log line, that the redaction
// marker sits where the code puts it, that the caller's context reaches the runner, and
// that the same input gives the same transcript on every run.
//
// These goldens record what the code does TODAY, defects included. A golden that has to
// change is a behaviour change and must be justified as one.
// Regenerate deliberately with:
//
//	go test ./internal/orchestrator/ -run TestPBSSectionCfgCharacterization -update
const (
	pbsSectionCharStageRoot = "/stage"
	pbsSectionCharCtxMarker = "pbs-section-char-ctx"
	// pbsSectionCharRepeats is how many times each case runs. The applies walk Go maps,
	// so an order that is not sorted on purpose would change from run to run: comparing
	// every run against the first catches it without depending on one lucky iteration.
	pbsSectionCharRepeats = 6

	// Distinctive secret values: none of them is a substring of anything else the
	// cases produce, so finding one in an error or a log line can only mean a leak.
	// remote.cfg stages the password base64-encoded, as PBS writes it, and the apply
	// passes it decoded: both forms are secrets.
	pbsSectionCharRemotePassword       = "Rm7pw-Qz4Xk9-remote-secret"
	pbsSectionCharRemotePasswordStaged = "Um03cHctUXo0WGs5LXJlbW90ZS1zZWNyZXQ="
	pbsSectionCharS3AccessKey          = "AKx3-Wd8Lp2-s3-access"
	pbsSectionCharS3SecretKey          = "Sk5v-Hj6Tn1-s3-secret"
)

var (
	errPBSSectionCharRead   = errors.New("injected read failure")
	errPBSSectionCharList   = errors.New("injected list failure")
	errPBSSectionCharCreate = errors.New("injected create failure")
	errPBSSectionCharUpdate = errors.New("injected update failure")
	errPBSSectionCharRemove = errors.New("injected remove failure")

	pbsSectionCharSentinels = []struct {
		name string
		err  error
	}{
		{"errPBSCleanRemoveIncomplete", errPBSCleanRemoveIncomplete},
		{"injected read failure", errPBSSectionCharRead},
		{"injected list failure", errPBSSectionCharList},
		{"injected create failure", errPBSSectionCharCreate},
		{"injected update failure", errPBSSectionCharUpdate},
		{"injected remove failure", errPBSSectionCharRemove},
	}

	pbsSectionCharTimestampRe = regexp.MustCompile(`^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\]`)
)

type pbsSectionCharCtxKey struct{}

// pbsSectionCharKind is one of the six applies. relPath, sectionType and body are the
// staged INPUT; listKeyFirst, secrets and redactedFlags are what the checks expect.
type pbsSectionCharKind struct {
	key          string
	relPath      string
	sectionType  string
	apply        func(context.Context, *logging.Logger, string, bool) error
	body         func(name string) []string
	listKeyFirst string
	// secrets are values that must reach the runner and never a returned error or a
	// log line.
	secrets []string
	// stagedSecrets are the staged forms of secrets that the apply decodes: they must
	// never reach a returned error or a log line either.
	stagedSecrets []string
	// redactedFlags are the flags whose value a failed create/update must show as
	// <redacted>.
	redactedFlags []string
}

var pbsSectionCharKinds = []pbsSectionCharKind{
	{
		key:         "remote",
		relPath:     "etc/proxmox-backup/remote.cfg",
		sectionType: "remote",
		apply:       applyPBSRemoteCfgViaAPI,
		body: func(n string) []string {
			return []string{
				"host " + n + ".pbs.example",
				"auth-id sync@pbs",
				"password " + pbsSectionCharRemotePasswordStaged,
				"fingerprint aa:bb:cc",
			}
		},
		listKeyFirst:  "id",
		secrets:       []string{pbsSectionCharRemotePassword},
		stagedSecrets: []string{pbsSectionCharRemotePasswordStaged},
		redactedFlags: []string{"--password"},
	},
	{
		key:         "s3",
		relPath:     "etc/proxmox-backup/s3.cfg",
		sectionType: "s3-endpoint",
		apply:       applyPBSS3CfgViaAPI,
		body: func(n string) []string {
			return []string{
				"endpoint " + n + ".s3.example",
				"region eu-central-1",
				"access-key " + pbsSectionCharS3AccessKey,
				"secret-key " + pbsSectionCharS3SecretKey,
			}
		},
		listKeyFirst:  "id",
		secrets:       []string{pbsSectionCharS3AccessKey, pbsSectionCharS3SecretKey},
		redactedFlags: []string{"--access-key", "--secret-key"},
	},
	{
		key:         "sync",
		relPath:     "etc/proxmox-backup/sync.cfg",
		sectionType: "sync",
		apply:       applyPBSSyncCfgViaAPI,
		body: func(n string) []string {
			return []string{
				"store ds-" + n,
				"remote origin",
				"remote-store src",
				"schedule hourly",
			}
		},
		listKeyFirst: "id",
	},
	{
		key:         "verification",
		relPath:     "etc/proxmox-backup/verification.cfg",
		sectionType: "verification",
		apply:       applyPBSVerificationCfgViaAPI,
		body: func(n string) []string {
			return []string{
				"store ds-" + n,
				"schedule daily",
				"ignore-verified true",
			}
		},
		listKeyFirst: "id",
	},
	{
		key:         "prune",
		relPath:     "etc/proxmox-backup/prune.cfg",
		sectionType: "prune",
		apply:       applyPBSPruneCfgViaAPI,
		body: func(n string) []string {
			return []string{
				"store ds-" + n,
				"schedule daily",
				"keep-last 3",
			}
		},
		listKeyFirst: "id",
	},
	{
		key:         "traffic-control",
		relPath:     "etc/proxmox-backup/traffic-control.cfg",
		sectionType: "rule",
		apply:       applyPBSTrafficControlCfgViaAPI,
		body: func(n string) []string {
			return []string{
				"network 10.0.0.0/8",
				"rate-in 10MB",
				"comment rule-" + n,
			}
		},
		listKeyFirst: "name",
	},
}

func (k pbsSectionCharKind) section(name string, extra ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s\n", k.sectionType, name)
	for _, line := range append(k.body(name), extra...) {
		b.WriteString("\t" + line + "\n")
	}
	return b.String()
}

// pbsSectionCharRule scripts the runner's answer to one command. verb is list, create,
// update or remove; id is the object the command names (empty matches any object, and
// list names none).
type pbsSectionCharRule struct {
	verb string
	id   string
	out  string
	err  error
}

type pbsSectionCharCall struct {
	method  string
	name    string
	args    []string
	ctxSeen bool
}

// pbsSectionCharRunner records every command with its argument boundaries intact
// (fakeCommandRunner joins them into one string, which would hide "s3 endpoint" passed
// as one argument) and answers through the first rule that matches.
type pbsSectionCharRunner struct {
	rules []pbsSectionCharRule
	calls []pbsSectionCharCall
}

func (r *pbsSectionCharRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.answer(ctx, "Run", name, args)
}

func (r *pbsSectionCharRunner) RunStdout(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.answer(ctx, "RunStdout", name, args)
}

func (r *pbsSectionCharRunner) answer(ctx context.Context, method, name string, args []string) ([]byte, error) {
	r.calls = append(r.calls, pbsSectionCharCall{
		method:  method,
		name:    name,
		args:    append([]string(nil), args...),
		ctxSeen: ctx != nil && ctx.Value(pbsSectionCharCtxKey{}) == pbsSectionCharCtxMarker,
	})
	verb, id := pbsSectionCharVerb(args)
	for _, rule := range r.rules {
		if rule.verb == verb && (rule.id == "" || rule.id == id) {
			if rule.err != nil {
				return nil, rule.err
			}
			return []byte(rule.out), nil
		}
	}
	return []byte{}, nil
}

// pbsSectionCharVerb finds the verb and the object it names, whatever the number of
// command words in front of it ("remote" or "s3 endpoint").
func pbsSectionCharVerb(args []string) (verb, id string) {
	for i, a := range args {
		switch a {
		case "list", "create", "update", "remove":
			if i+1 < len(args) {
				return a, args[i+1]
			}
			return a, ""
		}
	}
	return "", ""
}

// pbsSectionCharFS is the FakeFS with every read recorded, and failing on demand.
type pbsSectionCharFS struct {
	*FakeFS
	readErr error
	reads   []string
}

func (f *pbsSectionCharFS) ReadFile(path string) ([]byte, error) {
	f.reads = append(f.reads, path)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.FakeFS.ReadFile(path)
}

type pbsSectionCharScenario struct {
	name    string
	strict  bool
	staged  *string // nil: no staged file at all
	readErr bool
	rules   []pbsSectionCharRule
	check   func(t *testing.T, k pbsSectionCharKind, run pbsSectionCharRun)
}

type pbsSectionCharRun struct {
	calls []pbsSectionCharCall
	reads []string
	err   error
	log   []string
}

func pbsSectionCharStaged(sections ...string) *string {
	s := strings.Join(sections, "\n")
	return &s
}

// pbsSectionCharRows renders a list answer whose rows carry the same value under both
// "id" and "name", so only the key-order case can tell the two keys apart.
func pbsSectionCharRows(ids ...string) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(`{"id":%q,"name":%q}`, id, id))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// pbsSectionCharObjects returns the objects named by every command with the given verb,
// in the order the commands ran.
func pbsSectionCharObjects(calls []pbsSectionCharCall, verb string) []string {
	var out []string
	for _, c := range calls {
		if v, id := pbsSectionCharVerb(c.args); v == verb {
			out = append(out, id)
		}
	}
	return out
}

func pbsSectionCharErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func pbsSectionCharWantIs(t *testing.T, err error, target error, want bool) {
	t.Helper()
	if got := errors.Is(err, target); got != want {
		t.Errorf("errors.Is(err, %q) = %v, want %v (err: %v)", target, got, want, err)
	}
}

func pbsSectionCharWantObjects(t *testing.T, run pbsSectionCharRun, verb string, want ...string) {
	t.Helper()
	got := pbsSectionCharObjects(run.calls, verb)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s objects = %v, want %v", verb, got, want)
	}
}

func pbsSectionCharScenarios(k pbsSectionCharKind) []pbsSectionCharScenario {
	createFails := pbsSectionCharRule{verb: "create", id: "bravo", err: errPBSSectionCharCreate}
	updateFails := pbsSectionCharRule{verb: "update", id: "bravo", err: errPBSSectionCharUpdate}
	threeSections := pbsSectionCharStaged(k.section("alpha"), k.section("bravo"), k.section("charlie"))

	return []pbsSectionCharScenario{
		{
			name:   "absent",
			staged: nil,
		},
		{
			// Clean 1:1 with no staged file prunes nothing: the list never runs.
			name:   "absent-strict",
			strict: true,
			staged: nil,
			rules:  []pbsSectionCharRule{{verb: "list", out: pbsSectionCharRows("old-a")}},
		},
		{
			name:    "read-error",
			strict:  true,
			staged:  pbsSectionCharStaged(k.section("alpha")),
			readErr: true,
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantIs(t, run.err, errPBSSectionCharRead, true)
			},
		},
		{
			name:   "empty-file",
			staged: pbsSectionCharStaged("\n   \n\t\n"),
		},
		{
			// A present but empty file under Clean 1:1 means "no objects": every listed
			// object is removed.
			name:   "empty-file-strict",
			strict: true,
			staged: pbsSectionCharStaged("\n   \n\t\n"),
			rules:  []pbsSectionCharRule{{verb: "list", out: pbsSectionCharRows("old-b", "old-a")}},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantObjects(t, run, "remove", "old-a", "old-b")
				pbsSectionCharWantObjects(t, run, "create")
			},
		},
		{
			// A header with no name is not a header: the lines under the first one are
			// dropped, and the second one (with the lines under it) becomes flags of the
			// section above it.
			name:   "nameless-header",
			staged: pbsSectionCharStaged(k.sectionType+":\n\tcomment orphan-body\n", k.section("alpha"), k.sectionType+":\n\tcomment stray-body\n"),
		},
		{
			name:   "create-sorted",
			staged: pbsSectionCharStaged(k.section("charlie"), k.section("alpha"), k.section("delta"), k.section("bravo")),
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				got := pbsSectionCharObjects(run.calls, "create")
				if !sort.StringsAreSorted(got) {
					t.Errorf("create order %v is not sorted", got)
				}
				pbsSectionCharWantObjects(t, run, "create", "alpha", "bravo", "charlie", "delta")
				pbsSectionCharWantObjects(t, run, "list")
			},
		},
		{
			// digest and name are never passed; keys are lower-cased with "_" turned into
			// "-"; a value keeps its inner spaces as one argument.
			name: "flag-building",
			staged: pbsSectionCharStaged(k.section("alpha",
				"digest 0011aabbccdd",
				"name ignored-name",
				"Mixed_Case_Key   value  with  inner  spaces  ",
			)),
		},
		{
			// Two sections with one name: only the last one is applied.
			name:   "duplicate-section",
			staged: pbsSectionCharStaged(k.section("alpha"), k.section("alpha", "comment second-copy")),
		},
		{
			// The section type is never checked: a section of another type is applied too.
			name:   "foreign-section-type",
			staged: pbsSectionCharStaged(k.section("alpha"), "unrelated-type: zulu\n\tcomment from-elsewhere\n"),
		},
		{
			name:   "create-fails-update-ok",
			staged: threeSections,
			rules:  []pbsSectionCharRule{createFails},
		},
		{
			// The first object that fails both ways ends the apply: charlie is never tried.
			name:   "create-and-update-fail",
			staged: threeSections,
			rules:  []pbsSectionCharRule{createFails, updateFails},
			check: func(t *testing.T, k pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantIs(t, run.err, errPBSSectionCharCreate, false)
				pbsSectionCharWantIs(t, run.err, errPBSSectionCharUpdate, false)
				text := pbsSectionCharErrText(run.err)
				for _, flag := range k.redactedFlags {
					if n := strings.Count(text, flag+" <redacted>"); n != 2 {
						t.Errorf("%q followed by <redacted> appears %d times in the error, want 2 (create and update): %s", flag, n, text)
					}
				}
				if len(k.redactedFlags) == 0 && strings.Contains(text, "<redacted>") {
					t.Errorf("unexpected redaction in the error: %s", text)
				}
				// The secrets did reach the runner, so their absence from the error is
				// the redaction at work and not a case that never carried them.
				for _, secret := range k.secrets {
					found := false
					for _, c := range run.calls {
						for _, a := range c.args {
							if a == secret {
								found = true
							}
						}
					}
					if !found {
						t.Errorf("secret %q never reached the runner: the leak check would be vacuous", secret)
					}
				}
			},
		},
		{
			name:   "strict-list-fails",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules:  []pbsSectionCharRule{{verb: "list", err: errPBSSectionCharList}},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantIs(t, run.err, errPBSSectionCharList, true)
			},
		},
		{
			name:   "strict-list-invalid-json",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules:  []pbsSectionCharRule{{verb: "list", out: "not json"}},
		},
		{
			name:   "strict-list-row-without-id",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules:  []pbsSectionCharRule{{verb: "list", out: `[{"id":"alpha","name":"alpha"},{"comment":"no id","store":"ds"}]`}},
		},
		{
			name:   "strict-list-empty-output",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules:  []pbsSectionCharRule{{verb: "list", out: ""}},
		},
		{
			// Unsorted, with a duplicate: stale objects are removed once each, sorted,
			// before any create.
			name:   "strict-prune-plain-array",
			strict: true,
			staged: pbsSectionCharStaged(k.section("charlie"), k.section("alpha")),
			rules:  []pbsSectionCharRule{{verb: "list", out: pbsSectionCharRows("zeta", "alpha", "beta", "zeta")}},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantObjects(t, run, "remove", "beta", "zeta")
				pbsSectionCharWantObjects(t, run, "create", "alpha", "charlie")
			},
		},
		{
			name:   "strict-prune-data-wrapper",
			strict: true,
			staged: pbsSectionCharStaged(k.section("charlie"), k.section("alpha")),
			rules:  []pbsSectionCharRule{{verb: "list", out: `{"data":` + pbsSectionCharRows("zeta", "alpha", "beta", "zeta") + `}`}},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantObjects(t, run, "remove", "beta", "zeta")
				pbsSectionCharWantObjects(t, run, "create", "alpha", "charlie")
			},
		},
		{
			name:   "strict-remove-one-fails",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules: []pbsSectionCharRule{
				{verb: "list", out: pbsSectionCharRows("alpha", "beta", "gamma")},
				{verb: "remove", id: "beta", err: errPBSSectionCharRemove},
			},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantIs(t, run.err, errPBSCleanRemoveIncomplete, true)
				pbsSectionCharWantIs(t, run.err, errPBSSectionCharRemove, false)
				pbsSectionCharWantObjects(t, run, "remove", "beta", "gamma")
				pbsSectionCharWantObjects(t, run, "create", "alpha")
			},
		},
		{
			name:   "strict-remove-two-fail",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules: []pbsSectionCharRule{
				{verb: "list", out: pbsSectionCharRows("delta", "alpha", "beta", "gamma")},
				{verb: "remove", id: "beta", err: errPBSSectionCharRemove},
				{verb: "remove", id: "delta", err: errPBSSectionCharRemove},
			},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantIs(t, run.err, errPBSCleanRemoveIncomplete, true)
				pbsSectionCharWantObjects(t, run, "remove", "beta", "delta", "gamma")
			},
		},
		{
			// A create/update failure is returned instead of the incomplete clean: the
			// remove failure survives only as its Warning line.
			name:   "strict-remove-fails-then-create-fails",
			strict: true,
			staged: pbsSectionCharStaged(k.section("bravo")),
			rules: []pbsSectionCharRule{
				{verb: "list", out: pbsSectionCharRows("beta", "bravo")},
				{verb: "remove", id: "beta", err: errPBSSectionCharRemove},
				createFails,
				updateFails,
			},
			check: func(t *testing.T, _ pbsSectionCharKind, run pbsSectionCharRun) {
				pbsSectionCharWantIs(t, run.err, errPBSCleanRemoveIncomplete, false)
			},
		},
		{
			// Rows carrying both keys with different values show which key the kind reads
			// first; the other rows pin the fallback to the second key.
			name:   "strict-list-key-order",
			strict: true,
			staged: pbsSectionCharStaged(k.section("alpha")),
			rules: []pbsSectionCharRule{{verb: "list", out: `[` +
				`{"id":"alpha","name":"alpha"},` +
				`{"id":"id-x","name":"name-x"},` +
				`{"name":"only-name"},` +
				`{"id":"only-id"},` +
				`{"id":"","name":"blank-id"},` +
				`{"id":"blank-name","name":""},` +
				`{"id":7,"name":"numeric-id"},` +
				`{"id":"numeric-name","name":7}` +
				`]`}},
			check: func(t *testing.T, k pbsSectionCharKind, run pbsSectionCharRun) {
				first := "id-x"
				if k.listKeyFirst == "name" {
					first = "name-x"
				}
				want := []string{"blank-id", "blank-name", first, "numeric-id", "numeric-name", "only-id", "only-name"}
				sort.Strings(want)
				pbsSectionCharWantObjects(t, run, "remove", want...)
			},
		},
	}
}

func runPBSSectionCharScenario(t *testing.T, k pbsSectionCharKind, sc pbsSectionCharScenario) pbsSectionCharRun {
	t.Helper()

	origCmd, origFS := restoreCmd, restoreFS
	t.Cleanup(func() {
		restoreCmd = origCmd
		restoreFS = origFS
	})

	fake := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fake.Root) })
	if sc.staged != nil {
		if err := fake.WriteFile(filepath.Join(pbsSectionCharStageRoot, k.relPath), []byte(*sc.staged), 0o640); err != nil {
			t.Fatalf("write staged %s: %v", k.relPath, err)
		}
	}
	fsys := &pbsSectionCharFS{FakeFS: fake}
	if sc.readErr {
		fsys.readErr = errPBSSectionCharRead
	}
	restoreFS = fsys

	runner := &pbsSectionCharRunner{rules: sc.rules}
	restoreCmd = runner

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)

	ctx := context.WithValue(context.Background(), pbsSectionCharCtxKey{}, pbsSectionCharCtxMarker)
	err := k.apply(ctx, logger, pbsSectionCharStageRoot, sc.strict)

	var lines []string
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		lines = append(lines, pbsSectionCharTimestampRe.ReplaceAllString(line, "[<TIME>]"))
	}
	return pbsSectionCharRun{calls: runner.calls, reads: fsys.reads, err: err, log: lines}
}

func pbsSectionCharQuoteArg(a string) string {
	if a == "" || strings.ContainsAny(a, " \t\n\"'\\") {
		return strconv.Quote(a)
	}
	return a
}

func renderPBSSectionChar(sc pbsSectionCharScenario, run pbsSectionCharRun) string {
	var b strings.Builder
	fmt.Fprintf(&b, "### %s\n", sc.name)
	fmt.Fprintf(&b, "strict: %v\n", sc.strict)

	if sc.staged == nil {
		b.WriteString("staged: (no file)\n")
	} else {
		b.WriteString("staged:\n")
		for _, line := range strings.Split(*sc.staged, "\n") {
			fmt.Fprintf(&b, "  %s\n", strconv.Quote(line))
		}
	}
	if sc.readErr {
		fmt.Fprintf(&b, "read fails with: %v\n", errPBSSectionCharRead)
	}

	b.WriteString("answers:\n")
	if len(sc.rules) == 0 {
		b.WriteString("  (none: every command succeeds with no output)\n")
	}
	for _, rule := range sc.rules {
		target := rule.verb
		if rule.id != "" {
			target += " " + rule.id
		}
		if rule.err != nil {
			fmt.Fprintf(&b, "  %s -> error %q\n", target, rule.err.Error())
		} else {
			fmt.Fprintf(&b, "  %s -> output %q\n", target, rule.out)
		}
	}

	b.WriteString("reads:\n")
	if len(run.reads) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, p := range run.reads {
		fmt.Fprintf(&b, "  %s\n", p)
	}

	b.WriteString("commands:\n")
	if len(run.calls) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, c := range run.calls {
		parts := []string{pbsSectionCharQuoteArg(c.name)}
		for _, a := range c.args {
			parts = append(parts, pbsSectionCharQuoteArg(a))
		}
		fmt.Fprintf(&b, "  %s\n", strings.Join(parts, " "))
	}

	if run.err == nil {
		b.WriteString("error: (nil)\n")
	} else {
		fmt.Fprintf(&b, "error: %s\n", run.err.Error())
		var chain []string
		for e := run.err; e != nil; e = errors.Unwrap(e) {
			chain = append(chain, fmt.Sprintf("%T", e))
		}
		fmt.Fprintf(&b, "error chain: %s\n", strings.Join(chain, " -> "))
		var is []string
		for _, s := range pbsSectionCharSentinels {
			if errors.Is(run.err, s.err) {
				is = append(is, s.name)
			}
		}
		if len(is) == 0 {
			is = []string{"(none)"}
		}
		fmt.Fprintf(&b, "error is: %s\n", strings.Join(is, ", "))
	}

	b.WriteString("log:\n")
	if len(run.log) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, line := range run.log {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	return b.String()
}

// checkPBSSectionCharInvariants holds for every case of every kind.
func checkPBSSectionCharInvariants(t *testing.T, k pbsSectionCharKind, run pbsSectionCharRun) {
	t.Helper()
	for i, c := range run.calls {
		if c.name != "proxmox-backup-manager" {
			t.Errorf("call %d runs %q, want proxmox-backup-manager", i, c.name)
		}
		// Run merges stderr into the output that list parses as JSON.
		if c.method != "RunStdout" {
			t.Errorf("call %d went through %s, want RunStdout", i, c.method)
		}
		if !c.ctxSeen {
			t.Errorf("call %d did not receive the caller's context", i)
		}
	}
	errText := pbsSectionCharErrText(run.err)
	for _, secret := range append(append([]string(nil), k.secrets...), k.stagedSecrets...) {
		if strings.Contains(errText, secret) {
			t.Errorf("secret %q leaked into the returned error: %s", secret, errText)
		}
		for _, line := range run.log {
			if strings.Contains(line, secret) {
				t.Errorf("secret %q leaked into a log line: %s", secret, line)
			}
		}
	}
}

func TestPBSSectionCfgCharacterization(t *testing.T) {
	for _, k := range pbsSectionCharKinds {
		k := k
		t.Run(k.key, func(t *testing.T) {
			goldenPath := filepath.Join("testdata", "pbs_section_cfg_characterization", k.key+".golden")
			scenarios := pbsSectionCharScenarios(k)

			var golden map[string]string
			var goldenOrder []string
			if !*updateBackupGoldens {
				golden, goldenOrder = loadPBSSectionCharGolden(t, goldenPath)
			}

			blocks := make([]string, 0, len(scenarios))
			for _, sc := range scenarios {
				sc := sc
				t.Run(sc.name, func(t *testing.T) {
					first := runPBSSectionCharScenario(t, k, sc)
					got := renderPBSSectionChar(sc, first)
					for i := 1; i < pbsSectionCharRepeats; i++ {
						again := renderPBSSectionChar(sc, runPBSSectionCharScenario(t, k, sc))
						if again != got {
							t.Fatalf("run %d differs from run 0\n--- run 0 ---\n%s--- run %d ---\n%s", i, got, i, again)
						}
					}
					checkPBSSectionCharInvariants(t, k, first)
					if sc.check != nil {
						sc.check(t, k, first)
					}
					blocks = append(blocks, got)
					if *updateBackupGoldens {
						return
					}
					want, ok := golden[sc.name]
					if !ok {
						t.Fatalf("no block %q in %s (run with -update to create)", sc.name, goldenPath)
					}
					if want != got {
						t.Fatalf("golden mismatch for %s / %s\n--- want ---\n%s--- got ---\n%s", k.key, sc.name, want, got)
					}
				})
			}

			// The whole-file checks need every case: under -run that selects only some
			// of them there is nothing complete to write or to compare.
			if len(blocks) != len(scenarios) {
				if *updateBackupGoldens {
					t.Fatalf("%s: %d of %d cases produced a block; -update rewrites the whole file and needs them all", goldenPath, len(blocks), len(scenarios))
				}
				return
			}
			if *updateBackupGoldens {
				content := "# Characterization of " + k.relPath + " applied through proxmox-backup-manager.\n" +
					"# Generated by TestPBSSectionCfgCharacterization; regenerate with -update.\n\n" +
					strings.Join(blocks, "\n")
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("mkdir golden dir: %v", err)
				}
				if err := os.WriteFile(goldenPath, []byte(content), 0o644); err != nil {
					t.Fatalf("write golden %s: %v", goldenPath, err)
				}
				return
			}
			var names []string
			for _, sc := range scenarios {
				names = append(names, sc.name)
			}
			if strings.Join(goldenOrder, ",") != strings.Join(names, ",") {
				t.Fatalf("%s holds blocks %v, the cases are %v (run with -update)", goldenPath, goldenOrder, names)
			}
		})
	}
}

// loadPBSSectionCharGolden splits a golden file into its "### <case>" blocks.
func loadPBSSectionCharGolden(t *testing.T, path string) (map[string]string, []string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update to create): %v", path, err)
	}
	blocks := make(map[string]string)
	var order []string
	var current string
	var lines []string
	flush := func() {
		if current == "" {
			return
		}
		blocks[current] = strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "### ") {
			flush()
			current = strings.TrimPrefix(line, "### ")
			order = append(order, current)
			lines = []string{line}
			continue
		}
		if current != "" {
			lines = append(lines, line)
		}
	}
	flush()
	return blocks, order
}
