package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Every remote file a cloud retention deletes gets its DEBUG line, the archive and
// each file beside it.
func TestCloudRetentionLogsEveryDeletedRemoteFile(t *testing.T) {
	const kept = "node-backup-20241112-100000.tar.zst"
	const old = "node-backup-20241110-100000.tar.zst"
	remote := map[string]string{
		kept:                    "2024-11-12 10:00:00",
		kept + ".sha256":        "2024-11-12 10:00:00",
		kept + ".manifest.json": "2024-11-12 10:00:00",
		old:                     "2024-11-10 10:00:00",
		old + ".sha256":         "2024-11-10 10:00:00",
		old + ".manifest.json":  "2024-11-10 10:00:00",
	}
	exec := func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if len(args) == 0 {
			return nil, nil
		}
		target := strings.TrimPrefix(args[len(args)-1], "remote:")
		switch args[0] {
		case "lsl":
			names := make([]string, 0, len(remote))
			for name := range remote {
				names = append(names, name)
			}
			sort.Strings(names)
			var out strings.Builder
			for _, name := range names {
				fmt.Fprintf(&out, "%9d %s %s\n", 100, remote[name], name)
			}
			return []byte(out.String()), nil
		case "cat":
			return []byte(`{"hostname":"node"}`), nil
		case "deletefile":
			if _, ok := remote[target]; !ok {
				return []byte(target + ": Failed to deletefile: object not found"), errors.New("exit status 1")
			}
			delete(remote, target)
			return nil, nil
		}
		return nil, nil
	}

	logger := logging.New(types.LogLevelDebug, false)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	cs, err := NewCloudStorage(&config.Config{CloudEnabled: true, CloudRemote: "remote", CloudBatchSize: 1}, logger, "")
	if err != nil {
		t.Fatalf("NewCloudStorage: %v", err)
	}
	cs.hostname = "node"
	cs.sleep = func(time.Duration) {}
	cs.execCommand = exec

	deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
	if err != nil || deleted != 1 {
		t.Fatalf("ApplyRetention = %d, %v; want 1, nil", deleted, err)
	}
	for _, name := range []string{old, old + ".sha256", old + ".manifest.json"} {
		if !strings.Contains(buf.String(), "DEBUG    Cloud storage: deleted remote file "+name+"\n") {
			t.Fatalf("missing the DEBUG line for %s, log:\n%s", name, buf.String())
		}
	}
	if strings.Contains(buf.String(), "deleted remote file "+kept) {
		t.Fatalf("the kept backup must not be reported deleted, log:\n%s", buf.String())
	}
}
