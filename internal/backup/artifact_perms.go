package backup

import "os"

// Modes of the files of a backup set on BACKUP_PATH and SECONDARY_PATH: the archive,
// its .sha256, .metadata and .manifest.json, or the bundle that packs them.
//
// Every one of them is readable by root only, unless the operator asks otherwise.
// Group read is an opt-in: with SET_BACKUP_PERMISSIONS=true and both BACKUP_USER and
// BACKUP_GROUP set and resolvable on this host, the storage backends hand the files of
// the run to BACKUP_USER:BACKUP_GROUP at SharedArtifactFilePerm, and the permissions
// pass at startup does the same to the files earlier runs left. Cloud (rclone) copies
// are not covered: their mode is whatever the rclone backend gives them.
const (
	// ArtifactFilePerm is the mode every file of a backup set is created with, and the
	// mode the storage backends keep by default.
	ArtifactFilePerm os.FileMode = 0o600

	// SharedArtifactFilePerm is the mode the files of a backup set get when
	// SET_BACKUP_PERMISSIONS=true opens them to BACKUP_GROUP. Others get nothing.
	SharedArtifactFilePerm os.FileMode = 0o640
)
