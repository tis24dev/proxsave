package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// cancellingRunner records the commands it is asked to run and, on the one that
// matches cancelOn, cancels the restore context and fails the way a command killed
// by that cancellation does.
type cancellingRunner struct {
	calls    []string
	cancelOn string
	cancel   context.CancelFunc
}

func (r *cancellingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	r.calls = append(r.calls, call)
	if strings.HasPrefix(call, r.cancelOn) {
		r.cancel()
		return nil, errors.New("signal: killed")
	}
	if strings.Contains(call, " list ") {
		return []byte("[]"), nil
	}
	return nil, nil
}

// A cancellation during the apply of a section config stops it with the
// cancellation: no update attempt, no next object, no "failed (continuing)"
// warning, and the caller can tell it from a failed object.
func TestApplyPBSSectionCfgViaAPI_StopsOnCancellation(t *testing.T) {
	origFS, origCmd := restoreFS, restoreCmd
	t.Cleanup(func() { restoreFS, restoreCmd = origFS, origCmd })
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/prune.cfg", []byte("prune: p1\n\tstore ds1\n\nprune: p2\n\tstore ds1\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &cancellingRunner{cancelOn: "proxmox-backup-manager prune-job create p1", cancel: cancel}
	restoreCmd = runner

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	err := applyPBSPruneCfgViaAPI(ctx, logger, "/stage", true)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v; want context.Canceled", err)
	}
	want := []string{
		"proxmox-backup-manager prune-job list --output-format=json",
		"proxmox-backup-manager prune-job create p1 --store ds1",
	}
	if strings.Join(runner.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls\n%s\nwant\n%s", strings.Join(runner.calls, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("no warning expected on cancellation:\n%s", buf.String())
	}
}

// The staged apply returns the cancellation of a section config at once: no
// warning, no file-based fallback, and the restore step reads it as an abort.
func TestMaybeApplyPBSConfigsFromStage_SectionCancellationAbortsTheStep(t *testing.T) {
	origFS := restoreFS
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure, origRemote := pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyRemoteCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn, pbsStagedApplyRemoteCfgViaAPIFn = origEnsure, origRemote
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/remote.cfg", []byte("remote: r1\n\thost 10.0.0.10\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	pbsStagedApplyRemoteCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error {
		cancel()
		return errors.New("signal: killed")
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: PBSRestoreBehaviorClean, NormalCategories: []Category{{ID: "pbs_remotes"}}}
	w := &restoreUIWorkflowRun{ctx: ctx, logger: logger, plan: plan}
	step := restoreStageApplyStep{name: "PBS staged config apply", run: func() error {
		return maybeApplyPBSConfigsFromStage(w.ctx, w.logger, w.plan, "/stage", false)
	}}
	err := w.runStageApplyStep(step)
	if !restoreAbortOrInput(err) {
		t.Fatalf("runStageApplyStep = %v; want an abort", err)
	}
	if strings.Contains(buf.String(), "WARNING") {
		t.Fatalf("no warning expected on cancellation:\n%s", buf.String())
	}
	if _, statErr := fakeFS.Stat("/etc/proxmox-backup/remote.cfg"); statErr == nil {
		t.Fatal("the file-based fallback must not run after a cancellation")
	}
}

// pbsStderrMeasured are refusals proxmox-backup-manager 4.2.8 printed on VM 201
// (2026-10-17) for remote and s3 endpoint commands carrying a password or keys:
// none of them repeats the value it was given.
var pbsStderrMeasured = []string{
	"exit status 255: Error: parameter verification failed - 'fingerprint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager remote create <name> --auth-id <string> --host <string> --password <string> [OPTIONS]",
	"exit status 255: Error: parameter verification failed - 'name': remote 'r1' already exists.",
	"exit status 255: Error: parameter verification failed - 'endpoint': value does not match the regex pattern\n\nUsage: proxmox-backup-manager s3 endpoint create <id> --access-key <string> --endpoint <string> --secret-key <string> [OPTIONS]",
}

// A refused remote or s3 endpoint logs its warning without the secret it was
// given, neither as passed on the command line nor as stored in the file.
func TestApplyPBSSectionCfgViaAPI_RefusalWarningsCarryNoSecret(t *testing.T) {
	const secret = "SECRETPROBE4711"
	const secretB64 = "U0VDUkVUUFJPQkU0NzEx" // base64 of secret, as remote.cfg stores it
	cases := []struct {
		name  string
		file  string
		cfg   string
		apply func(context.Context, *logging.Logger, string, bool) error
		cmd   string
	}{
		{"remote", "remote.cfg", "remote: r1\n\thost 127.0.0.1\n\tauth-id root@pam\n\tpassword " + secretB64 + "\n", applyPBSRemoteCfgViaAPI, "proxmox-backup-manager remote"},
		{"s3", "s3.cfg", "s3-endpoint: e1\n\tendpoint s3.example.com\n\taccess-key AK" + secret + "\n\tsecret-key SK" + secret + "\n", applyPBSS3CfgViaAPI, "proxmox-backup-manager s3 endpoint"},
	}
	for _, tc := range cases {
		for i, stderr := range pbsStderrMeasured {
			origFS, origCmd := restoreFS, restoreCmd
			fakeFS := NewFakeFS()
			restoreFS = fakeFS
			_ = fakeFS.WriteFile("/stage/etc/proxmox-backup/"+tc.file, []byte(tc.cfg), 0o640)
			runner := &FakeCommandRunner{Outputs: map[string][]byte{}, Errors: map[string]error{}}
			runner.Outputs[tc.cmd+" list --output-format=json"] = []byte("[]")
			restoreCmd = &refusingRunner{inner: runner, prefix: tc.cmd + " ", stderr: stderr}
			var buf bytes.Buffer
			logger := logging.New(types.LogLevelDebug, false)
			logger.SetOutput(&buf)
			err := tc.apply(context.Background(), logger, "/stage", true)
			restoreFS, restoreCmd = origFS, origCmd
			_ = os.RemoveAll(fakeFS.Root)
			if err == nil {
				t.Fatalf("%s/%d: the refusal must be reported", tc.name, i)
			}
			text := buf.String() + err.Error()
			if !strings.Contains(buf.String(), "WARNING") {
				t.Fatalf("%s/%d: expected the refusal warning, log:\n%s", tc.name, i, buf.String())
			}
			if strings.Contains(text, secret) || strings.Contains(text, secretB64) {
				t.Fatalf("%s/%d: the secret leaked:\n%s", tc.name, i, text)
			}
		}
	}
}

// refusingRunner answers the list from inner and refuses every create and update
// with the given stderr, the way osCommandRunner reports a failed command.
type refusingRunner struct {
	inner  *FakeCommandRunner
	prefix string
	stderr string
}

func (r *refusingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	call := strings.Join(append([]string{name}, args...), " ")
	if strings.HasPrefix(call, r.prefix) && !strings.Contains(call, " list ") {
		return nil, errors.New(r.stderr)
	}
	return r.inner.Run(ctx, name, args...)
}
