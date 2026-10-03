package block

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/safeexec"
)

const testRepository = "proxsave-probe@pbs!pve@127.0.0.1:proxsave-probe"

// Measured on pve-test against a closed port (rc 255 in 0.05 s).
const measuredConnectRefusedStderr = "Error: client error (Connect)\n" +
	"Caused by: error connecting to https://127.0.0.1:8008/ - tcp connect error: Connection refused (os error 111)\n"

// The client must be reachable through the real allowlist, which the fake client below
// also goes through: a fake runner would accept any name.
func TestPBSClientIsAllowedBySafeexec(t *testing.T) {
	if _, err := safeexec.CommandContext(context.Background(), pbsClientName, "version"); err != nil {
		t.Fatalf("%s is not allowed by safeexec: %v", pbsClientName, err)
	}
}

func TestClientRunEnvironmentHoldsOnlyTheStorageSecrets(t *testing.T) {
	fake := installFakeClient(t)
	// What a host exporting the collector's PBS variables would hand to every child.
	t.Setenv("PBS_REPOSITORY", "root@pam@other:ds")
	t.Setenv("PBS_PASSWORD", "collector-secret")
	t.Setenv("PBS_FINGERPRINT", "00:00")
	t.Setenv("PBS_ENCRYPTION_PASSWORD", "collector-passphrase")
	fake.expect(t, "PBS_PASSWORD", testSecret)
	fake.expect(t, "PBS_FINGERPRINT", testServerFP)

	result := Client{Password: testSecret, Fingerprint: testServerFP}.Run(context.Background(), "status", "--repository", testRepository, "--output-format", "json")
	if result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	calls := fake.calls(t)
	if len(calls) != 1 {
		t.Fatalf("got %d calls, want 1", len(calls))
	}
	call := calls[0]
	if got := strings.Join(call.PBSEnv, ","); got != "PBS_FINGERPRINT,PBS_PASSWORD" && got != "PBS_PASSWORD,PBS_FINGERPRINT" {
		t.Errorf("PBS_* received = %v, want only PBS_PASSWORD and PBS_FINGERPRINT", call.PBSEnv)
	}
	if !call.Match["PBS_PASSWORD"] || !call.Match["PBS_FINGERPRINT"] {
		t.Errorf("client received the parent's values, not the storage's: %v", call.Match)
	}
	for _, arg := range call.Args {
		if strings.Contains(arg, testSecret) {
			t.Fatalf("the password is in argv: %q", call.Args)
		}
	}
}

func TestClientRunWithoutFingerprintPassesNone(t *testing.T) {
	fake := installFakeClient(t)
	t.Setenv("PBS_FINGERPRINT", "00:00")
	if result := (Client{Password: testSecret}).Run(context.Background(), "status"); result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	if got := strings.Join(fake.calls(t)[0].PBSEnv, ","); got != "PBS_PASSWORD" {
		t.Fatalf("PBS_* received = %q, want PBS_PASSWORD only", got)
	}
}

func TestClientRunStdinIsTheNullDevice(t *testing.T) {
	fake := installFakeClient(t)
	// Under go test the process stdin is often /dev/null already: give it a pipe, so a
	// client that inherited ProxSave's stdin would be told apart.
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	defer reader.Close()
	defer writer.Close()
	original := os.Stdin
	os.Stdin = reader
	defer func() { os.Stdin = original }()

	if result := (Client{Password: testSecret}).Run(context.Background(), "status"); result.Err != nil {
		t.Fatalf("Run: %v", result.Err)
	}
	if got := fake.calls(t)[0].Stdin; got != "/dev/null" {
		t.Fatalf("client stdin = %q, want /dev/null", got)
	}
}

func TestClientRunKeepsArgsOutputAndExitCode(t *testing.T) {
	fake := installFakeClient(t)
	fake.respond(t, "status", 255, "", measuredConnectRefusedStderr)
	args := []string{"status", "--repository", testRepository, "--output-format", "json"}
	result := Client{Password: testSecret}.Run(context.Background(), args...)

	if strings.Join(fake.calls(t)[0].Args, " ") != strings.Join(args, " ") {
		t.Errorf("argv = %q, want %q", fake.calls(t)[0].Args, args)
	}
	if strings.Join(result.Args, " ") != strings.Join(args, " ") {
		t.Errorf("result.Args = %q, want %q", result.Args, args)
	}
	if result.ExitCode != 255 {
		t.Errorf("ExitCode = %d, want 255", result.ExitCode)
	}
	var exitErr *exec.ExitError
	if !errors.As(result.Err, &exitErr) {
		t.Errorf("Err = %v, want an *exec.ExitError", result.Err)
	}
	if string(result.Stderr) != measuredConnectRefusedStderr || len(result.Stdout) != 0 {
		t.Errorf("stdout/stderr = %q/%q", result.Stdout, result.Stderr)
	}
	if result.NotFound() {
		t.Error("NotFound() on a client that ran")
	}
}

func TestClientRunSuccessKeepsStdout(t *testing.T) {
	fake := installFakeClient(t)
	// Measured `status --output-format json` on pve-test.
	status := `{"avail":8186691584,"backend-type":"filesystem","total":30080253952,"used":20560846848}` + "\n"
	fake.respond(t, "status", 0, status, "")
	result := Client{Password: testSecret}.Run(context.Background(), "status", "--output-format", "json")
	if result.Err != nil || result.ExitCode != 0 || string(result.Stdout) != status {
		t.Fatalf("Err=%v ExitCode=%d Stdout=%q", result.Err, result.ExitCode, result.Stdout)
	}
}

// No timeout of ProxSave's own: the call ends when the run context does.
func TestClientRunStopsWhenTheContextEnds(t *testing.T) {
	fake := installFakeClient(t)
	fake.hang(t, "backup", 30)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := Client{Password: testSecret}.Run(ctx, "backup", "proxsave.pxar:/tmp/tree")
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Run returned after %s, the client was not stopped", elapsed)
	}
	if !errors.Is(result.Err, context.DeadlineExceeded) {
		t.Fatalf("Err = %v, want it to wrap context.DeadlineExceeded", result.Err)
	}
	if result.ExitCode != -1 {
		t.Fatalf("ExitCode = %d, want -1 for a killed client", result.ExitCode)
	}

	fake.hang(t, "prune", 30)
	ctx, cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	result = Client{Password: testSecret}.Run(ctx, "prune")
	if !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("Err = %v, want it to wrap context.Canceled", result.Err)
	}
}

func TestClientRunNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	result := Client{Password: testSecret}.Run(context.Background(), "version")
	if !result.NotFound() {
		t.Fatalf("NotFound() = false, Err = %v", result.Err)
	}
	if got := ClientNotFound(result.Err).Line(); got != "proxmox-backup-client: not found" {
		t.Fatalf("fact = %q", got)
	}
}

// Measured shape of `version --repository <repo> --output-format json`.
func versionJSONOutput(clientVersion, clientRelease, serverVersion, serverRelease string) string {
	return `{"client":{"release":"` + clientRelease + `","version":"` + clientVersion + `"},` +
		`"server":{"release":"` + serverRelease + `","version":"` + serverVersion + `"}}` + "\n"
}

func TestProbeVersion(t *testing.T) {
	fake := installFakeClient(t)
	fake.respond(t, "version", 0, versionJSONOutput("4.2", "5", "3.4", "9"), "")
	versions, result, err := Client{Password: testSecret}.ProbeVersion(context.Background(), testRepository)
	if err != nil {
		t.Fatalf("ProbeVersion: %v", err)
	}
	if versions.Client.String() != "4.2.5" || versions.Server.String() != "3.4.9" {
		t.Fatalf("versions = client %s server %s", versions.Client, versions.Server)
	}
	want := "version --repository " + testRepository + " --output-format json"
	if got := strings.Join(fake.calls(t)[0].Args, " "); got != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
	if result.ExitCode != 0 {
		t.Fatalf("ExitCode = %d", result.ExitCode)
	}
}

func TestProbeVersionFailure(t *testing.T) {
	fake := installFakeClient(t)
	fake.respond(t, "version", 255, "", measuredConnectRefusedStderr)
	_, result, err := Client{Password: testSecret}.ProbeVersion(context.Background(), testRepository)
	if err == nil || result.ExitCode != 255 || string(result.Stderr) != measuredConnectRefusedStderr {
		t.Fatalf("err=%v ExitCode=%d Stderr=%q", err, result.ExitCode, result.Stderr)
	}

	fake.respond(t, "version", 0, "client version: 4.2.5\n", "")
	if _, _, err := (Client{Password: testSecret}).ProbeVersion(context.Background(), testRepository); err == nil {
		t.Fatal("output that is not the JSON was accepted")
	}
}

// Measured: clients 3.0.1, 3.1.2, 3.2.2, 3.2.3, 3.2.4 reject --change-detection-mode;
// 3.2.5 to 3.4.9 and 4.2.5 accept it.
func TestSupportsChangeDetectionMode(t *testing.T) {
	for _, tc := range []struct {
		version, release string
		want             bool
	}{
		{"3.0", "1", false}, {"3.1", "2", false}, {"3.2", "2", false}, {"3.2", "3", false}, {"3.2", "4", false},
		{"3.2", "5", true}, {"3.2", "9", true}, {"3.3", "0", true}, {"3.4", "9", true}, {"4.2", "5", true},
		{"2.4", "7", false},
	} {
		versions, err := ParseVersionOutput([]byte(versionJSONOutput(tc.version, tc.release, "4.2", "5")))
		if err != nil {
			t.Fatalf("ParseVersionOutput(%s.%s): %v", tc.version, tc.release, err)
		}
		if got := versions.Client.SupportsChangeDetectionMode(); got != tc.want {
			t.Errorf("client %s: SupportsChangeDetectionMode = %v, want %v", versions.Client, got, tc.want)
		}
	}
}
