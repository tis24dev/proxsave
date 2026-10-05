package orchestrator

// blockBackup is a backup block. A block receives the collected workspace and
// produces the backup in its own destination. A returned error is critical and
// stops the run.
type blockBackup interface {
	execute(run *backupRunContext, workspace *backupWorkspace) error
}
