package orchestrator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The rollback scripts take LOG, MARKER and BACKUP from BASE_DIR, which comes from the
// executable's path. shellQuote quoted a value only when it held whitespace or one of
// "'\$&;|<>, so a BASE_DIR with ( ) or a backtick made every rollback script a
// syntax error in /bin/sh ("(" unexpected): the rollback never ran. Each value now
// reaches the shell as the same string.
func TestRollbackScriptsKeepEveryBaseDirCharacter(t *testing.T) {
	builders := map[string]func(marker, backup, log string) string{
		"network":        func(m, b, l string) string { return buildRollbackScript(m, b, l, false) },
		"firewall":       buildFirewallRollbackScript,
		"ha":             buildHARollbackScript,
		"access-control": buildAccessControlRollbackScript,
	}
	for _, base := range []string{"/opt/proxsave", "/srv/proxsave(main)", "/srv/prox`id`save", "/srv/it's here", "/srv/a*b?[c]#~{d}"} {
		marker := "/tmp/proxsave/rollback.marker"
		backup := base + "/restore/20261028-120000/rollback.tar.gz"
		log := base + "/restore/20261028-120000/rollback.log"
		for name, build := range builders {
			script := build(marker, backup, log)
			path := filepath.Join(t.TempDir(), "rollback.sh")
			if err := os.WriteFile(path, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
				t.Fatalf("%s, BASE_DIR %q: /bin/sh -n: %v: %s", name, base, err, out)
			}
			var assigns []string
			for line := range strings.SplitSeq(script, "\n") {
				if strings.HasPrefix(line, "LOG=") || strings.HasPrefix(line, "MARKER=") || strings.HasPrefix(line, "BACKUP=") {
					assigns = append(assigns, line)
				}
			}
			out, err := exec.Command("/bin/sh", "-c", strings.Join(assigns, "\n")+"\nprintf '%s\\n' \"$LOG\" \"$MARKER\" \"$BACKUP\"").CombinedOutput()
			if err != nil {
				t.Fatalf("%s, BASE_DIR %q: assignments: %v: %s", name, base, err, out)
			}
			if got, want := string(out), log+"\n"+marker+"\n"+backup+"\n"; got != want {
				t.Fatalf("%s, BASE_DIR %q: the shell read\n%s\nwant\n%s", name, base, got, want)
			}
		}
	}
}
