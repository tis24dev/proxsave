package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/cli"
	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// flippingHostnameOnPath installs a "hostname" that answers a DIFFERENT name on
// every call: probe-1.example.invalid, probe-2.example.invalid, and so on. It is
// the mechanism of discussion #292 made deterministic. The real probe depends on
// getaddrinfo, /etc/hosts and DNS, any of which can change or fail between two
// calls in one process, so a second resolution is not a free repeat of the first.
//
// It reuses fakeHostnameOnPath (run_hostname_timeout_test.go), which PREPENDS its own
// directory to PATH rather than replacing it, so the script can still find the
// utilities it calls. The counter file lives in a separate temp dir to keep the
// fixture's own scratch state out of a directory that is on PATH for the duration of
// the test, not because LookPath would otherwise pick the wrong file.
func flippingHostnameOnPath(t *testing.T) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "calls")
	if err := os.WriteFile(counter, []byte("0\n"), 0o600); err != nil {
		t.Fatalf("seed probe counter: %v", err)
	}
	fakeHostnameOnPath(t, `n=$(cat `+counter+`)
n=$((n+1))
echo "$n" > `+counter+`
echo "probe-$n.example.invalid"`)
}

// TestRestoreSupportBundleNamesTheHostTheRunUsed pins the READING side of
// discussion #292 on the support bundle.
//
// The run resolves its name once (initializeRunLogFile assigns
// rt.hostname = resolveHostname(), main_runtime.go) and every consumer is handed
// THAT value. restoreSupportStats used to call resolveHostname() a second time, so
// the bundle could name a host the run itself never used, which is precisely the
// artefact an operator sends when they are debugging a hostname problem: the log
// name, the archives and the access control check all say one name while the
// bundle header says another.
//
// The fixture makes the two resolutions differ on purpose. On an ordinary machine
// they usually agree, which is why this defect survived: a test that trusts the
// host's own name would stay green with the bug in place on every developer
// machine and in CI.
func TestRestoreSupportBundleNamesTheHostTheRunUsed(t *testing.T) {
	flippingHostnameOnPath(t)

	// The run's single resolution, exactly as initializeRunLogFile performs it.
	runName := resolveHostname()
	if runName == "" {
		t.Fatalf("the fixture probe answered nothing; this test needs a name to pin")
	}

	logger := logging.New(types.LogLevelWarning, false)
	logger.SetOutput(io.Discard)
	rt := &appRuntime{
		args:        &cli.Args{Restore: true, Support: true},
		logger:      logger,
		envInfo:     &environment.EnvironmentInfo{Type: types.ProxmoxVE, Version: "8.2"},
		toolVersion: "0.0.0-test",
		hostname:    runName,
		startTime:   time.Now(),
	}

	stats := restoreSupportStats(rt, types.ExitSuccess.Int())
	if stats == nil {
		t.Fatal("restoreSupportStats returned nil for a --support run; the bundle would carry no stats at all")
	}
	if stats.Hostname != runName {
		t.Fatalf("the support bundle names %q while the run used %q: a second resolveHostname() call can answer differently from the first (it shells out to \"hostname -f\" and falls back to the kernel name), so the bundle an operator sends while debugging a hostname problem names a host that never ran (discussion #292)", stats.Hostname, runName)
	}

	// Prove the fixture really does flip, so the assertion above cannot pass by
	// accident on a host where two resolutions happen to agree. Whether the code
	// under test probed once more or not, the next answer differs from the first.
	if again := resolveHostname(); again == runName {
		t.Fatalf("the flipping probe answered %q twice; this test proves nothing unless the two resolutions differ", again)
	}
}

// TestRestoreSupportBundleNeverShipsANamelessHost covers the one way reading
// rt.hostname could be worse than resolving a second time: a bundle naming nobody.
//
// rt.hostname cannot be empty on any path that reaches restoreSupportStats today.
// initializeRunLogFile assigns it as its FIRST statement, above the
// "if rt.args.Restore { return }" guard, bootstrapRuntime calls that
// unconditionally before it hands a runtime back, and resolveHostname has no return
// path yielding "" (it falls back to the "unknown" sentinel). Three existing guards
// keep it that way: the first row of TestRunHostnameWiredIntoPinnedCallSites,
// TestInitializeRunLogFileAssignsTheRunHostnameInBothModes (whose restore row is
// exactly this concern) and TestBootstrapRuntimeNamesEveryRunItHandsBack.
//
// So this is the belt to that braces, in the house style of runHostnameOrReport
// (backup_execution.go): if the plumb is ever cut, the bundle still names the
// machine AND the run says out loud that the name it shipped is not the name it
// used. Silence there is what let discussion #292 reach a release.
func TestRestoreSupportBundleNeverShipsANamelessHost(t *testing.T) {
	fakeHostnameOnPath(t, `echo "recovered.example.invalid"`)

	logger := logging.New(types.LogLevelWarning, false)
	logger.SetOutput(io.Discard)
	rt := &appRuntime{
		args:        &cli.Args{Restore: true, Support: true},
		logger:      logger,
		envInfo:     &environment.EnvironmentInfo{Type: types.ProxmoxVE, Version: "8.2"},
		toolVersion: "0.0.0-test",
		hostname:    "",
		startTime:   time.Now(),
	}

	stats := restoreSupportStats(rt, types.ExitSuccess.Int())
	if stats == nil {
		t.Fatal("restoreSupportStats returned nil for a --support run; the bundle would carry no stats at all")
	}
	if stats.Hostname != "recovered.example.invalid" {
		t.Fatalf("support bundle hostname = %q, want the recovered name: a bundle that names no host at all is worse than one that names the host twice, because nothing in it identifies the machine that produced it", stats.Hostname)
	}
	if n := logger.WarningCount(); n != 1 {
		t.Fatalf("a dropped hostname plumb logged %d warning(s), want exactly 1: the fallback must say that the name in the bundle is not the name the run used, or a cut plumb stays invisible exactly like discussion #292 did", n)
	}
}

// TestRestoreSupportStatsStaySilentOnAWiredRun is the control for the warning above:
// an ordinary run must log nothing at all.
//
// The reason is NOT the exit code, and saying so would be wrong: the restore path
// never reaches applyIssueExitCode (it is called from internal/orchestrator's backup
// stats path alone), so this warning cannot promote a restore to a non-zero status.
// The reason is that the warning is the ONLY signal that the bundle is named by a
// second probe rather than by the run. Emitted on every ordinary restore it would be
// noise, and a signal that fires always carries nothing.
func TestRestoreSupportStatsStaySilentOnAWiredRun(t *testing.T) {
	logger := logging.New(types.LogLevelWarning, false)
	logger.SetOutput(io.Discard)
	rt := &appRuntime{
		args:        &cli.Args{Restore: true, Support: true},
		logger:      logger,
		envInfo:     &environment.EnvironmentInfo{Type: types.ProxmoxVE, Version: "8.2"},
		toolVersion: "0.0.0-test",
		hostname:    "pve.home.arpa",
		startTime:   time.Now(),
	}

	if stats := restoreSupportStats(rt, types.ExitSuccess.Int()); stats == nil || stats.Hostname != "pve.home.arpa" {
		t.Fatalf("restoreSupportStats did not carry the run's name through unchanged: %+v", stats)
	}
	if n := logger.WarningCount(); n != 0 {
		t.Fatalf("a correctly wired run logged %d warning(s); only a dropped plumb may say anything", n)
	}
}
