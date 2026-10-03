package block

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeClient is testdata/fake-proxmox-backup-client.sh installed as proxmox-backup-client
// in a temporary directory put first in PATH. The code under test reaches it through the
// real safeexec.CommandContext, allowlist included: a fake runner would accept any name.
type fakeClient struct {
	dir string
}

// fakeCall is one recorded invocation.
type fakeCall struct {
	Args   []string
	PBSEnv []string        // names of the PBS_* variables received
	Match  map[string]bool // expect.<NAME>: whether the received value was the expected one
	Stdin  string          // target of fd 0
}

func installFakeClient(t *testing.T) *fakeClient {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("testdata", "fake-proxmox-backup-client.sh"))
	if err != nil {
		t.Fatalf("read fake client: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, pbsClientName), script, 0o755); err != nil {
		t.Fatalf("install fake client: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &fakeClient{dir: dir}
}

func (f *fakeClient) write(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// respond makes the subcommand key ("version", "snapshot-list", ...) answer with rc,
// stdout and stderr.
func (f *fakeClient) respond(t *testing.T, key string, rc int, stdout, stderr string) {
	t.Helper()
	f.write(t, key+".rc", strconv.Itoa(rc))
	f.write(t, key+".stdout", stdout)
	f.write(t, key+".stderr", stderr)
}

// hang makes the subcommand key sleep for seconds after its output.
func (f *fakeClient) hang(t *testing.T, key string, seconds int) {
	t.Helper()
	f.write(t, key+".sleep", strconv.Itoa(seconds))
}

// expect makes the fake compare the value of the variable name with value.
func (f *fakeClient) expect(t *testing.T, name, value string) {
	t.Helper()
	f.write(t, "expect."+name, value)
}

func (f *fakeClient) calls(t *testing.T) []fakeCall {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "calls.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read calls.log: %v", err)
	}
	var calls []fakeCall
	var current *fakeCall
	for _, line := range strings.Split(string(data), "\n") {
		kind, value, _ := strings.Cut(line, " ")
		switch kind {
		case "call":
			calls = append(calls, fakeCall{Match: map[string]bool{}})
			current = &calls[len(calls)-1]
		case "arg":
			current.Args = append(current.Args, value)
		case "env":
			current.PBSEnv = append(current.PBSEnv, value)
		case "match":
			name, verdict, _ := strings.Cut(value, " ")
			current.Match[name] = verdict == "yes"
		case "stdin":
			current.Stdin = value
		}
	}
	return calls
}
