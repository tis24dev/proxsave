// Package block holds the destination blocks that receive the collected workspace after
// the path block (Local, Secondary, Cloud) has run. Every block here is non critical: it
// reports an outcome and never stops the run.
package block

import (
	"context"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/storage"
)

// Outcome vocabulary of Result.Status, the same words SecondaryStatus and CloudStatus use.
const (
	StatusOK      = "ok"
	StatusWarning = "warning"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Input is what every destination block receives. The tree is read-only.
//
// There is no deadline of the block's own: the run is bounded only by the daemon's
// MAX_RUN_DURATION watchdog, which reaches the block through Ctx.
type Input struct {
	Ctx       context.Context // run context, cancelled on SIGINT/SIGTERM
	TreeDir   string          // collected workspace (backupWorkspace.tempDir), read-only
	Hostname  string          // run identity as in bundle names (FQDN)
	StartTime time.Time       // run start, also the lower bound of the post-upload check
	ServerID  string
	DryRun    bool
	Logger    *logging.Logger
}

// Result is the outcome of one destination.
type Result struct {
	Name            string // operator-visible name ("PBS Storage")
	Status          string // StatusOK | StatusWarning | StatusError | StatusSkipped
	Location        string // display form of the destination (repository + namespace)
	Snapshot        string // host/<backup-id>/<RFC3339>, empty when none was created
	Backups         int    // own snapshots after retention; -1 = unknown
	MaxBackups      int    // simple policy limit, for "N/M"
	RetentionPolicy string // simple | gfs
	// GFS tiers of a gfs RetentionPolicy, as the retention pass applies them
	// (storage.EffectiveGFSRetentionConfig); zero for simple.
	GFSDaily   int    `json:",omitempty"`
	GFSWeekly  int    `json:",omitempty"`
	GFSMonthly int    `json:",omitempty"`
	GFSYearly  int    `json:",omitempty"`
	Deleted    int    // snapshots removed by this run's retention
	FreeBytes  uint64 // datastore filesystem, from `status`
	UsedBytes  uint64
	TotalBytes uint64
}

// SetRetention writes the retention of the destination into the result: the policy,
// the simple limit and, for GFS, the tiers the retention pass applies.
func (r *Result) SetRetention(rc storage.RetentionConfig) {
	r.RetentionPolicy = rc.Policy
	r.MaxBackups = rc.MaxBackups
	r.GFSDaily, r.GFSWeekly, r.GFSMonthly, r.GFSYearly = 0, 0, 0, 0
	if rc.Policy == "gfs" {
		effective := storage.EffectiveGFSRetentionConfig(rc)
		r.GFSDaily, r.GFSWeekly, r.GFSMonthly, r.GFSYearly = effective.Daily, effective.Weekly, effective.Monthly, effective.Yearly
	}
}

// Backup is a non-critical destination block: it never aborts the run.
type Backup interface {
	Name() string
	Execute(in Input) Result
}
