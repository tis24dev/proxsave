package orchestrator

import (
	"os"
	"testing"
)

// fakeFSRoot holds every FakeFS directory of this test binary, so the ones a test
// does not remove go away with the run instead of piling up in the system temp dir.
var fakeFSRoot string

func TestMain(m *testing.M) {
	root, err := os.MkdirTemp("", "proxsave-orchestrator-test-")
	if err != nil {
		panic(err)
	}
	fakeFSRoot = root
	code := m.Run()
	_ = os.RemoveAll(root)
	os.Exit(code)
}
