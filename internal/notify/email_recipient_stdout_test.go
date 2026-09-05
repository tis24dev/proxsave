package notify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Measured on a live PVE 9.1.9 node on 2026-09-05, with the locale as the only
// variable changed. Both PVE detection tiers run the same Perl, so both produce
// this block on stderr while their JSON on stdout stays intact and the exit code
// stays 0.
const measuredPerlLocaleWarning = "perl: warning: Setting locale failed.\n" +
	"perl: warning: Please check that your locale settings:\n" +
	"\tLC_ALL = \"xx_YY.UTF-8\",\n" +
	"\tLANG = \"xx_YY.UTF-8\"\n" +
	"    are supported and installed on your system.\n" +
	"perl: warning: Falling back to the standard locale (\"C\").\n"

// stubCapturedOutput installs a runner that answers a command line with a stdout
// body and a stderr body, the way a real process does.
func stubCapturedOutput(t *testing.T, stdout, stderr map[string]string, seen *[]string) {
	t.Helper()
	orig := runCapturedOutput
	t.Cleanup(func() { runCapturedOutput = orig })
	runCapturedOutput = func(_ context.Context, name string, args ...string) ([]byte, []byte, error) {
		key := strings.TrimSpace(name + " " + strings.Join(args, " "))
		if seen != nil {
			*seen = append(*seen, key)
		}
		return []byte(stdout[key]), []byte(stderr[key]), nil
	}
}

// Tier 1 of the PVE recipient lookup. Merged, the parse fails on 'p' (from
// "perl:") and the whole tier is reported as broken.
func TestRecipientViaPveshSurvivesALocaleWarningOnStderr(t *testing.T) {
	const cmd = "pvesh get /access/users/root@pam --output-format=json"
	var seen []string
	stubCapturedOutput(t,
		map[string]string{cmd: `{"userid":"root@pam","email":"ops@example.test"}`},
		map[string]string{cmd: measuredPerlLocaleWarning},
		&seen)

	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxVE, logging.New(types.LogLevelError, false))
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	got, err := notifier.detectRecipientPVEViaPvesh(context.Background(), "root@pam")
	if err != nil {
		t.Fatalf("a locale warning on stderr hid the recipient: %v", err)
	}
	if got != "ops@example.test" {
		t.Fatalf("email = %q", got)
	}
	if len(seen) != 1 || seen[0] != cmd {
		t.Fatalf("unexpected commands: %v", seen)
	}
}

// Tier 2. It runs pveum, which is the same Perl as pvesh, so a locale the node
// lacks takes both tiers out at once and only the user.cfg fallback answers.
func TestRecipientViaUserListSurvivesALocaleWarningOnStderr(t *testing.T) {
	const cmd = "pveum user list --output-format json"
	stubCapturedOutput(t,
		map[string]string{cmd: `[{"userid":"root@pam","email":"ops@example.test"}]`},
		map[string]string{cmd: measuredPerlLocaleWarning},
		nil)

	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxVE, logging.New(types.LogLevelError, false))
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	got, err := notifier.detectRecipientViaUserListCLI(context.Background(), "pveum", []string{"user", "list", "--output-format", "json"}, "root@pam")
	if err != nil {
		t.Fatalf("a locale warning on stderr hid the recipient: %v", err)
	}
	if got != "ops@example.test" {
		t.Fatalf("email = %q", got)
	}
}

// The diagnostic must still reach the log when the command fails. stderr is where
// the reason normally is; stdout is the fallback, never a merge.
func TestCommandDiagnosticPrefersStderrThenStdout(t *testing.T) {
	if got := commandDiagnostic([]byte("on stdout"), []byte("  on stderr  ")); got != "on stderr" {
		t.Fatalf("got %q", got)
	}
	if got := commandDiagnostic([]byte(" on stdout "), []byte("   ")); got != "on stdout" {
		t.Fatalf("got %q", got)
	}
	if got := commandDiagnostic(nil, nil); got != "" {
		t.Fatalf("got %q", got)
	}
}

// mockNoisyCmd puts a real executable on PATH that writes body to stdout and noise
// to stderr and exits 0, the way pvesh does under a locale the node lacks.
func mockNoisyCmd(t *testing.T, name, stdoutBody, stderrBody string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\ncat >&2 <<'STDERR_EOF'\n" + stderrBody + "STDERR_EOF\ncat <<'STDOUT_EOF'\n" + stdoutBody + "\nSTDOUT_EOF\nexit 0\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write mock command: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The tests above stub runCapturedOutput, so they pin what the CALLERS do with two
// streams; they cannot see whether the runner actually splits them. This one runs a
// real process through the real runner and is the test that fails if the capture
// goes back to merging.
func TestRunCapturedOutputKeepsTheStreamsApart(t *testing.T) {
	mockNoisyCmd(t, "pvesh", `{"userid":"root@pam","email":"ops@example.test"}`, measuredPerlLocaleWarning)

	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxVE, logging.New(types.LogLevelError, false))
	if err != nil {
		t.Fatalf("NewEmailNotifier() error=%v", err)
	}

	got, err := notifier.detectRecipientPVEViaPvesh(context.Background(), "root@pam")
	if err != nil {
		t.Fatalf("a real locale warning on stderr hid the recipient: %v", err)
	}
	if got != "ops@example.test" {
		t.Fatalf("email = %q", got)
	}

	// And directly, so a future caller refactor cannot quietly retire the guarantee.
	stdout, stderr, err := runCapturedOutput(context.Background(), "pvesh", "get", "/access/users/root@pam", "--output-format=json")
	if err != nil {
		t.Fatalf("runner failed: %v", err)
	}
	if strings.Contains(string(stdout), "perl:") {
		t.Fatalf("stderr leaked into stdout: %q", stdout)
	}
	if !strings.Contains(string(stderr), "Setting locale failed") {
		t.Fatalf("stderr was dropped instead of returned: %q", stderr)
	}
}
