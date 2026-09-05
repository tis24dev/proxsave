package orchestrator

import (
	"context"
	"strings"
	"testing"
)

// Measured on a live PVE 9.1.9 node on 2026-09-05, with the ONLY variable changed
// being the locale:
//
//	baseline              rc=0 stdout=548B stderr=0B
//	LC_ALL=xx_YY.UTF-8    rc=0 stdout=548B stderr=548B  "perl: warning: Setting locale failed."
//
// pvesh exits 0 and the JSON on stdout is intact either way. CombinedOutput merges
// the two, the merged bytes start with 'p', and json.Unmarshal fails. The blast
// radius is not one guest: loadPVEGuestInventory failing sets failed = len(entries)
// and refuses EVERY guest config, and pveshGuestStatus failing skips the file
// fallback for the one it was probing.
//
// It is reached without anything exotic. That node ships the stock
// `AcceptEnv LANG LC_*` in sshd_config and has 4 locales generated; macOS Terminal
// forwards LC_CTYPE=UTF-8 by default. An operator restoring over ssh from a Mac is
// the whole reproduction.
// The warning VERBATIM, captured from that node on 2026-09-05 (556 bytes). It is
// kept whole rather than abridged because the count of lines is itself evidence:
// a parser that splits on newlines and takes the first field, as listPVEPoolIDs
// does, gets 17 distinct phantom entries out of it, one per variable enumerated
// here. An abridged sample would understate the damage by a factor of four.
const measuredPerlLocaleWarning = "perl: warning: Setting locale failed.\n" +
	"perl: warning: Please check that your locale settings:\n" +
	"\tLANGUAGE = (unset),\n" +
	"\tLC_ALL = \"xx_YY.UTF-8\",\n" +
	"\tLC_CTYPE = (unset),\n" +
	"\tLC_NUMERIC = (unset),\n" +
	"\tLC_COLLATE = (unset),\n" +
	"\tLC_TIME = (unset),\n" +
	"\tLC_MESSAGES = (unset),\n" +
	"\tLC_MONETARY = (unset),\n" +
	"\tLC_ADDRESS = (unset),\n" +
	"\tLC_IDENTIFICATION = (unset),\n" +
	"\tLC_MEASUREMENT = (unset),\n" +
	"\tLC_PAPER = (unset),\n" +
	"\tLC_TELEPHONE = (unset),\n" +
	"\tLC_NAME = (unset),\n" +
	"\tLANG = \"en_US.UTF-8\"\n" +
	"    are supported and installed on your system.\n" +
	"perl: warning: Falling back to a fallback locale (\"en_US.UTF-8\").\n"

// localeNoisyPvesh answers the way the live node answered: Run returns the merged
// stream (what CombinedOutput would hand back), RunStdout returns only the data.
type localeNoisyPvesh struct {
	stdout string
	calls  []string
}

func (p *localeNoisyPvesh) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	p.calls = append(p.calls, "Run "+name+" "+strings.Join(args, " "))
	return []byte(measuredPerlLocaleWarning + p.stdout), nil
}

func (p *localeNoisyPvesh) RunStdout(_ context.Context, name string, args ...string) ([]byte, error) {
	p.calls = append(p.calls, "RunStdout "+name+" "+strings.Join(args, " "))
	return []byte(p.stdout), nil
}

func TestGuestInventorySurvivesALocaleWarningOnStderr(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })
	noisy := &localeNoisyPvesh{stdout: `[{"vmid":101,"node":"pve","type":"qemu","status":"stopped","name":"openwrt25"}]`}
	restoreCmd = noisy

	inventory, err := loadPVEGuestInventory(context.Background())
	if err != nil {
		t.Fatalf("a locale warning on stderr refused the whole inventory: %v", err)
	}
	if got, ok := inventory["101"]; !ok || got.Node != "pve" || got.Kind != "qemu" {
		t.Fatalf("inventory did not parse: %+v", inventory)
	}
	if calls := strings.Join(noisy.calls, "\n"); !strings.Contains(calls, "RunStdout pvesh") {
		t.Fatalf("the inventory still reads the merged stream:\n%s", calls)
	}
}

func TestGuestStatusSurvivesALocaleWarningOnStderr(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })
	noisy := &localeNoisyPvesh{stdout: `{"status":"stopped","vmid":101}`}
	restoreCmd = noisy

	status, err := pveshGuestStatus(context.Background(), "pve", vmEntry{VMID: "101", Kind: "qemu"})
	if err != nil {
		t.Fatalf("a locale warning on stderr hid the guest status: %v", err)
	}
	if status != "stopped" {
		t.Fatalf("status = %q, want stopped", status)
	}
}

// The fallback exists so no fake has to grow a method it does not need. A runner
// that only implements Run must still be driven, exactly as before.
func TestStdoutHelperFallsBackToRunWhenUnsupported(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })
	restoreCmd = &FakeCommandRunner{Outputs: map[string][]byte{"pvesh get /x": []byte("plain")}}

	out, err := runCommandStdout(context.Background(), "pvesh", "get", "/x")
	if err != nil {
		t.Fatalf("fallback failed: %v", err)
	}
	if string(out) != "plain" {
		t.Fatalf("fallback returned %q", out)
	}
}
