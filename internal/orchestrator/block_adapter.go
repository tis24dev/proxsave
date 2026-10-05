package orchestrator

import (
	"fmt"

	"github.com/tis24dev/proxsave/internal/block"
)

// blockAdapter runs a destination block (block.Backup) as a numbered step of its own
// after the path block: the header, the block, then its outcome in BackupStats. It never
// returns an error: a destination block is not critical.
type blockAdapter struct {
	o    *Orchestrator
	b    block.Backup
	step int
}

func (a *blockAdapter) execute(run *backupRunContext, workspace *backupWorkspace) error {
	o := a.o
	fmt.Println()
	if o.dryRun {
		o.logStep(a.step, "%s dispatch skipped (dry run mode)", a.b.Name())
	} else {
		o.logStep(a.step, "%s - dispatching archive", a.b.Name())
	}
	result := a.b.Execute(block.Input{
		Ctx:       run.ctx,
		TreeDir:   workspace.tempDir,
		Hostname:  run.hostname,
		StartTime: run.startTime,
		ServerID:  o.serverID,
		DryRun:    o.dryRun,
		Logger:    o.logger,
	})
	o.logger.Debug("%s: status=%s snapshot=%q backups=%d deleted=%d", result.Name, result.Status, result.Snapshot, result.Backups, result.Deleted)
	if run.stats != nil {
		run.stats.PBSTarget = &result
	}
	return nil
}

// blockSkipped stands for a destination step that is switched off in the
// configuration: one SKIP line in place of the step, the numbering goes on.
type blockSkipped struct {
	o    *Orchestrator
	name string
}

func (s *blockSkipped) execute(*backupRunContext, *backupWorkspace) error {
	fmt.Println()
	s.o.logger.Skip("%s: disabled", s.name)
	return nil
}

// pbsStep is the number of the PBS step, between the storage dispatch [6] and the
// notifications [8].
const pbsStep = 7

// destinationBlocks are the steps after the path block: the registered PBS block, or
// its SKIP line when PBS_TARGET_ENABLED=false.
func (o *Orchestrator) destinationBlocks() []blockBackup {
	if len(o.backupBlocks) == 0 {
		if o.cfg != nil && !o.cfg.PBSTargetEnabled {
			return []blockBackup{&blockSkipped{o: o, name: block.PBSName}}
		}
		return nil
	}
	blocks := make([]blockBackup, 0, len(o.backupBlocks))
	for _, b := range o.backupBlocks {
		blocks = append(blocks, &blockAdapter{o: o, b: b, step: pbsStep})
	}
	return blocks
}
