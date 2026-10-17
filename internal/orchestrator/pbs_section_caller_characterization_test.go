package orchestrator

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// Regenerate deliberately with:
// go test ./internal/orchestrator/ -run TestPBSSectionCallerCharacterization -update-section-caller
var updateSectionCallerGoldens = flag.Bool("update-section-caller", false, "rewrite PBS section caller golden files")

type sectionCallerTarget struct {
	name     string
	category string
	file     string // staged and target file of its fallback
	hook     *func(context.Context, *logging.Logger, string, bool) error
}

func sectionCallerTargets() []sectionCallerTarget {
	return []sectionCallerTarget{
		{"traffic-control", "pbs_host", "traffic-control.cfg", &pbsStagedApplyTrafficControlCfgViaAPIFn},
		{"s3", "datastore_pbs", "s3.cfg", &pbsStagedApplyS3CfgViaAPIFn},
		{"remote", "pbs_remotes", "remote.cfg", &pbsStagedApplyRemoteCfgViaAPIFn},
		{"sync", "pbs_jobs", "sync.cfg", &pbsStagedApplySyncCfgViaAPIFn},
		{"verification", "pbs_jobs", "verification.cfg", &pbsStagedApplyVerificationCfgViaAPIFn},
		{"prune", "pbs_jobs", "prune.cfg", &pbsStagedApplyPruneCfgViaAPIFn},
	}
}

var sectionCallerStamp = regexp.MustCompile(`^\[[^\]]*\] `)
var sectionCallerDuration = regexp.MustCompile(`duration=[^)]*\)`)

// The handling of a failed API apply of each section config by the staged apply:
// the full log and the returned error, for a generic error with the fallback
// applied, with the fallback failing, for a Clean 1:1 incomplete error, and in
// Merge mode.
func TestPBSSectionCallerCharacterization(t *testing.T) {
	cases := []struct {
		name          string
		behavior      PBSRestoreBehavior
		err           error
		fallbackFails bool
	}{
		{"clean-fallback-applied", PBSRestoreBehaviorClean, errors.New("forced API error"), false},
		{"clean-fallback-fails", PBSRestoreBehaviorClean, errors.New("forced API error"), true},
		{"clean-incomplete", PBSRestoreBehaviorClean, fmt.Errorf("x remove failed for 1 object(s) [old]: %w", errPBSCleanRemoveIncomplete), false},
		{"merge", PBSRestoreBehaviorMerge, errors.New("forced API error"), false},
	}
	for _, tg := range sectionCallerTargets() {
		for _, tc := range cases {
			t.Run(tg.name+"/"+tc.name, func(t *testing.T) {
				got := runSectionCallerCase(t, tg, tc.behavior, tc.err, tc.fallbackFails)
				path := filepath.Join("testdata", "pbs_section_caller", tg.name+"."+tc.name+".golden")
				if *updateSectionCallerGoldens {
					if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read golden %s (run with -update-section-caller): %v", path, err)
				}
				if got != string(want) {
					t.Fatalf("golden %s mismatch\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
				}
			})
		}
	}
}

func runSectionCallerCase(t *testing.T, tg sectionCallerTarget, behavior PBSRestoreBehavior, apiErr error, fallbackFails bool) string {
	t.Helper()
	origFS := restoreFS
	origIsReal, origGeteuid := pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn
	origEnsure := pbsStagedApplyEnsurePBSServicesForAPIFn
	saved := map[*func(context.Context, *logging.Logger, string, bool) error]func(context.Context, *logging.Logger, string, bool) error{}
	for _, other := range sectionCallerTargets() {
		saved[other.hook] = *other.hook
	}
	origNode, origDS := pbsStagedApplyNodeCfgViaAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn
	t.Cleanup(func() {
		restoreFS = origFS
		pbsStagedApplyIsRealRestoreFSFn, pbsStagedApplyGeteuidFn = origIsReal, origGeteuid
		pbsStagedApplyEnsurePBSServicesForAPIFn = origEnsure
		for hook, fn := range saved {
			*hook = fn
		}
		pbsStagedApplyNodeCfgViaAPIFn, pbsStagedApplyDatastoreCfgViaAPIFn = origNode, origDS
	})
	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS
	pbsStagedApplyIsRealRestoreFSFn = func(FS) bool { return true }
	pbsStagedApplyGeteuidFn = func() int { return 0 }
	pbsStagedApplyEnsurePBSServicesForAPIFn = func(context.Context, *logging.Logger) error { return nil }
	for _, other := range sectionCallerTargets() {
		*other.hook = func(context.Context, *logging.Logger, string, bool) error { return nil }
	}
	pbsStagedApplyNodeCfgViaAPIFn = func(context.Context, string) error { return nil }
	pbsStagedApplyDatastoreCfgViaAPIFn = func(context.Context, *logging.Logger, string, bool) error { return nil }
	*tg.hook = func(context.Context, *logging.Logger, string, bool) error { return apiErr }

	if err := fakeFS.WriteFile("/stage/etc/proxmox-backup/"+tg.file, []byte(strings.TrimSuffix(tg.file, ".cfg")+": obj1\n\tcomment c\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	if fallbackFails {
		// A directory where the fallback writes the file: the write fails.
		if err := fakeFS.MkdirAll("/etc/proxmox-backup/"+tg.file+"/x", 0o755); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelDebug, false)
	logger.SetOutput(&buf)
	plan := &RestorePlan{SystemType: SystemTypePBS, PBSRestoreBehavior: behavior, NormalCategories: []Category{{ID: tg.category}}}
	err := maybeApplyPBSConfigsFromStage(context.Background(), logger, plan, "/stage", false)

	var out strings.Builder
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		line = sectionCallerStamp.ReplaceAllString(line, "")
		line = sectionCallerDuration.ReplaceAllString(line, "duration=<d>)")
		line = strings.ReplaceAll(line, fakeFS.Root, "<fakefs>")
		line = regexp.MustCompile(`proxsave\.tmp\.[0-9]+`).ReplaceAllString(line, "proxsave.tmp.<n>")
		out.WriteString(line + "\n")
	}
	out.WriteString(fmt.Sprintf("returned: %v\n", err))
	return out.String()
}
