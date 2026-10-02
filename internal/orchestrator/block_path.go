package orchestrator

// blockPath is the path block: it creates the archive, verifies it, bundles it
// and copies it to the registered storage targets.
type blockPath struct{ o *Orchestrator }

func (b *blockPath) execute(run *backupRunContext, workspace *backupWorkspace) error {
	o := b.o
	artifacts, err := o.createBackupArchive(run, workspace)
	if err != nil {
		return err
	}
	if err := o.verifyAndWriteBackupArtifacts(run, workspace, artifacts); err != nil {
		return err
	}
	if err := o.bundleBackupArtifacts(run, workspace, artifacts); err != nil {
		return err
	}
	o.finalizeBackupStats(run)
	return o.dispatchBackupArtifacts(run)
}
