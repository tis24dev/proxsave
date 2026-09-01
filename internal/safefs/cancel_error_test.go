package safefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A cancelled context must not lose the file. Every other shape a bounded helper
// returns names the operation and the path - "remove /x: permission denied",
// "remove /x: timeout after 30s", "remove /x: timeout" - and only the cancelled
// one used to hand back a bare "context canceled" (normalizeContextErr's
// non-deadline arm). A caller that wants to name the file in a log line then has
// to repeat it from its own arguments, which prints it twice on the three shapes
// that already carry it.
//
// The identity must survive: 19 production sites ask errors.Is(err,
// context.Canceled) and IsAbandoned reads the same answer, so the new error
// unwraps rather than replaces.
func TestCancelledOperationNamesTheOperationAndPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "x.tar")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name string
		call func() error
		want string
	}{
		{"remove", func() error { return Remove(ctx, file, 30*time.Second) }, "remove " + file + ": canceled"},
		{"stat", func() error { _, err := Stat(ctx, file, 30*time.Second); return err }, "stat " + file + ": canceled"},
		{"chmod", func() error { return Chmod(ctx, file, 0o600, 30*time.Second) }, "chmod " + file + ": canceled"},
		{"mkdirall", func() error { return MkdirAll(ctx, filepath.Join(dir, "sub"), 0o700, 30*time.Second) }, "mkdirall " + filepath.Join(dir, "sub") + ": canceled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			if err == nil {
				t.Fatal("want an error on a cancelled context")
			}
			if got := err.Error(); got != tc.want {
				t.Errorf("text = %q, want %q", got, tc.want)
			}
			if !errors.Is(err, context.Canceled) {
				t.Errorf("errors.Is(err, context.Canceled) = false; 19 production sites depend on it")
			}
			if !IsAbandoned(err) {
				t.Errorf("IsAbandoned = false; callers skip closing their handles on this answer")
			}
		})
	}
}

// A context error that is neither Canceled nor DeadlineExceeded must pass through
// untouched. Turning it into a cancel error would flip IsAbandoned to true, and
// that answer tells a caller NOT to close its file handles.
func TestAForeignContextErrorIsNotRewritten(t *testing.T) {
	ctx := foreignErrContext{Context: context.Background()}
	err := Remove(ctx, filepath.Join(t.TempDir(), "absent.tar"), 30*time.Second)
	if err == nil || err.Error() != "mount evacuated by the storage layer" {
		t.Fatalf("err = %v, want the context's own error verbatim", err)
	}
	if errors.Is(err, context.Canceled) || IsAbandoned(err) {
		t.Errorf("a foreign context error must not read as an abandonment: Is=%v Abandoned=%v",
			errors.Is(err, context.Canceled), IsAbandoned(err))
	}
}

type foreignErrContext struct{ context.Context }

func (foreignErrContext) Err() error { return errors.New("mount evacuated by the storage layer") }
func (foreignErrContext) Done() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
