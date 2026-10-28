package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
)

// Simple retention on the cloud deletes the expired backups oldest first, as the
// folder destinations do: a pass stopped midway has removed the oldest and kept the
// newer ones. It deleted them newest first.
func TestCloudSimpleRetentionDeletesOldestFirst(t *testing.T) {
	cfg := &config.Config{CloudEnabled: true, CloudRemote: "remote", BundleAssociatedFiles: true, CloudBatchSize: 10}
	cs := newCloudStorageForTest(cfg)
	cs.hostname = "node"
	cs.hostAliases = nil
	var deleted []string
	cs.execCommand = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		switch args[0] {
		case "lsl":
			return []byte("4 2024-11-04 01:00:00 node-backup-20241104-010000.tar.zst.bundle.tar\n" +
				"4 2024-11-03 01:00:00 node-backup-20241103-010000.tar.zst.bundle.tar\n" +
				"4 2024-11-02 01:00:00 node-backup-20241102-010000.tar.zst.bundle.tar\n" +
				"4 2024-11-01 01:00:00 node-backup-20241101-010000.tar.zst.bundle.tar\n"), nil
		case "deletefile":
			if f := args[len(args)-1]; strings.HasSuffix(f, ".bundle.tar") {
				deleted = append(deleted, f[strings.LastIndex(f, "node-backup-")+len("node-backup-"):][:8])
			}
			return nil, nil
		case "cat":
			return nil, errors.New("ERROR : object not found")
		}
		return nil, nil
	}
	if _, err := cs.ApplyRetention(context.Background(), RetentionConfig{Policy: "simple", MaxBackups: 1}); err != nil {
		t.Fatalf("ApplyRetention: %v", err)
	}
	if strings.Join(deleted, ",") != "20241101,20241102,20241103" {
		t.Fatalf("deleted in order %v, want oldest first", deleted)
	}
}
