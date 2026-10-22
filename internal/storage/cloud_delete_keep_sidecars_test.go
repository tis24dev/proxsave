package storage

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
)

// TestCloudRetentionKeepsSidecarsWhenArchiveDeleteFails pins that a cloud backup whose
// archive cannot be deleted loses none of the files beside it, so the next pass still
// lists it as verified and retries it, instead of skipping it for "no manifest"
// forever. The remote is a stateful fake: what pass 1 deletes is gone in pass 2.
func TestCloudRetentionKeepsSidecarsWhenArchiveDeleteFails(t *testing.T) {
	const kept = "node-backup-20241112-100000.tar.zst"
	const blocked = "node-backup-20241110-100000.tar.zst"
	sidecars := []string{blocked + ".sha256", blocked + ".manifest.json", blocked + ".metadata"}
	remote := map[string]string{
		kept:                    "2024-11-12 10:00:00",
		kept + ".sha256":        "2024-11-12 10:00:00",
		kept + ".manifest.json": "2024-11-12 10:00:00",
		blocked:                 "2024-11-10 10:00:00",
	}
	for _, s := range sidecars {
		remote[s] = "2024-11-10 10:00:00"
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
			if _, ok := remote[target]; !ok {
				return []byte(target + ": object not found"), errors.New("exit status 1")
			}
			return []byte(`{"hostname":"node"}`), nil
		case "deletefile":
			if _, ok := remote[target]; !ok {
				return []byte(target + ": Failed to deletefile: object not found"), errors.New("exit status 1")
			}
			if target == blocked {
				return []byte("permission denied"), errors.New("exit status 1")
			}
			delete(remote, target)
			return nil, nil
		}
		return nil, nil
	}

	for pass := 1; pass <= 2; pass++ {
		cs := newCloudStorageForTest(&config.Config{CloudEnabled: true, CloudRemote: "remote", CloudBatchSize: 1})
		cs.hostname = "node"
		cs.sleep = func(time.Duration) {}
		cs.execCommand = exec

		deleted, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1})
		if err != nil || deleted != 0 {
			t.Fatalf("pass %d: ApplyRetention = %d, %v; want 0, nil", pass, deleted, err)
		}
		if cs.retTally.skipped != 0 || cs.retTally.planned != 1 || cs.retTally.notDeleted != 1 {
			t.Fatalf("pass %d: skipped=%d planned=%d notDeleted=%d; want 0, 1, 1 (the backup is retried)",
				pass, cs.retTally.skipped, cs.retTally.planned, cs.retTally.notDeleted)
		}
		for _, s := range sidecars {
			if _, ok := remote[s]; !ok {
				t.Fatalf("pass %d: %s was deleted", pass, s)
			}
		}
	}
}
