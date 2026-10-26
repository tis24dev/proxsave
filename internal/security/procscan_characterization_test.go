package security

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests pin readProcInfo as it behaves today, value by value, against what the
// kernel reports through plain /proc reads in the test itself. They are the safety
// net for reading comm, status and exe through one handle on /proc/<pid>, so they
// assert the three values, never how they are read.

// referenceProcInfo computes the values readProcInfo must return, with plain reads.
func referenceProcInfo(t *testing.T, pid int) procInfo {
	t.Helper()
	dir := "/proc/" + strconv.Itoa(pid)
	want := procInfo{}
	if data, err := os.ReadFile(dir + "/comm"); err == nil {
		want.comm = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile(dir + "/status"); err == nil {
		for line := range strings.SplitSeq(string(data), "\n") {
			if rest, ok := strings.CutPrefix(line, "PPid:"); ok {
				want.ppid, _ = strconv.Atoi(strings.TrimSpace(rest))
				break
			}
		}
	}
	if target, err := filepath.EvalSymlinks(dir + "/exe"); err == nil {
		want.exe = target
	}
	return want
}

func assertProcInfo(t *testing.T, got, want procInfo) {
	t.Helper()
	if got != want {
		t.Fatalf("readProcInfo = {comm:%q ppid:%d exe:%q}, want {comm:%q ppid:%d exe:%q}",
			got.comm, got.ppid, got.exe, want.comm, want.ppid, want.exe)
	}
}

// copySleep copies the system sleep binary into dir under name and returns its path.
func copySleep(t *testing.T, dir, name string) string {
	t.Helper()
	src, err := exec.LookPath("sleep")
	if err != nil {
		t.Skipf("sleep not available: %v", err)
	}
	in, err := os.Open(src)
	if err != nil {
		t.Fatalf("open %s: %v", src, err)
	}
	defer func() { _ = in.Close() }()
	dst := filepath.Join(dir, name)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatalf("create %s: %v", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		t.Fatalf("copy: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return dst
}

// startSleeper starts bin with a long sleep and kills it at cleanup. A binary that was
// just written can briefly fail with ETXTBSY while another goroutine of the test
// binary forks with the write descriptor inherited, so that error is retried.
func startSleeper(t *testing.T, bin string) *exec.Cmd {
	t.Helper()
	var cmd *exec.Cmd
	var err error
	for range 50 {
		cmd = exec.Command(bin, "60")
		if err = cmd.Start(); !errors.Is(err, syscall.ETXTBSY) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("start %s: %v", bin, err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd
}

func TestReadProcInfoCurrentProcessCharacterization(t *testing.T) {
	pid := os.Getpid()
	got := readProcInfo(pid)
	assertProcInfo(t, got, referenceProcInfo(t, pid))
	if got.ppid != os.Getppid() {
		t.Fatalf("ppid = %d, want os.Getppid() = %d", got.ppid, os.Getppid())
	}
	if got.comm == "" || got.exe == "" {
		t.Fatalf("current process must have comm and exe, got %+v", got)
	}
}

// A child started from a binary in a temp directory: comm is its file name, ppid is
// this test process, exe is the binary's resolved path.
func TestReadProcInfoChildCharacterization(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	bin := copySleep(t, dir, "char-sleeper")
	cmd := startSleeper(t, bin)

	got := readProcInfo(cmd.Process.Pid)
	assertProcInfo(t, got, procInfo{comm: "char-sleeper", ppid: os.Getpid(), exe: bin})
}

// A child whose binary was deleted after it started: the kernel reports the exe link
// as "<path> (deleted)", which does not resolve, so exe stays empty while comm and
// ppid are still read.
func TestReadProcInfoDeletedBinaryCharacterization(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	bin := copySleep(t, dir, "char-deleted")
	cmd := startSleeper(t, bin)
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove %s: %v", bin, err)
	}
	link, err := os.Readlink("/proc/" + strconv.Itoa(cmd.Process.Pid) + "/exe")
	if err != nil || !strings.HasSuffix(link, " (deleted)") {
		t.Skipf("kernel does not report the deleted binary as expected: link=%q err=%v", link, err)
	}

	got := readProcInfo(cmd.Process.Pid)
	assertProcInfo(t, got, procInfo{comm: "char-deleted", ppid: os.Getpid(), exe: ""})
}

// A process that already exited and was reaped yields the zero value.
func TestReadProcInfoReapedProcessCharacterization(t *testing.T) {
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skipf("true not runnable: %v", err)
	}
	assertProcInfo(t, readProcInfo(cmd.Process.Pid), procInfo{})
}

func TestReadProcInfoNonPositivePIDCharacterization(t *testing.T) {
	for _, pid := range []int{0, -1} {
		assertProcInfo(t, readProcInfo(pid), procInfo{})
	}
}

// pid 1 belongs to another user when the tests do not run as root: comm and status
// are world readable, exe usually is not. Whatever the kernel allows, readProcInfo
// returns exactly that.
func TestReadProcInfoInitCharacterization(t *testing.T) {
	if _, err := os.Stat("/proc/1"); err != nil {
		t.Skipf("no /proc/1: %v", err)
	}
	got := readProcInfo(1)
	assertProcInfo(t, got, referenceProcInfo(t, 1))
	if got.ppid != 0 {
		t.Fatalf("pid 1 ppid = %d, want 0", got.ppid)
	}
}
