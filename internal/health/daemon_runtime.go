package health

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const DaemonRuntimeSchemaVersion = 1

type DaemonRuntimePathComponent struct {
	Path string `json:"path"`
	UID  uint32 `json:"uid"`
	Mode uint32 `json:"mode"`
}

type DaemonRuntimeScript struct {
	Path       string                       `json:"path"`
	State      string                       `json:"state"`
	Reason     string                       `json:"reason,omitempty"`
	Components []DaemonRuntimePathComponent `json:"components,omitempty"`
}

type DaemonRuntimeScripts struct {
	Pre  DaemonRuntimeScript `json:"pre"`
	Post DaemonRuntimeScript `json:"post"`
}

type DaemonRuntimeState struct {
	SchemaVersion   int                  `json:"schema_version"`
	PID             int                  `json:"pid"`
	StartTS         int64                `json:"start_ts"`
	ConfigPath      string               `json:"config_path"`
	DaemonUID       int                  `json:"daemon_uid"`
	PersonalScripts DaemonRuntimeScripts `json:"personal_scripts"`
}

func DaemonRuntimePath(baseDir string) string {
	return filepath.Join(baseDir, "identity", ".daemon_runtime.json")
}

func WriteDaemonRuntime(baseDir string, state DaemonRuntimeState) error {
	return writeJSONAtomic(DaemonRuntimePath(baseDir), state)
}

func ReadDaemonRuntime(baseDir string) (DaemonRuntimeState, bool, error) {
	data, err := os.ReadFile(DaemonRuntimePath(baseDir))
	if err != nil {
		if os.IsNotExist(err) {
			return DaemonRuntimeState{}, false, nil
		}
		return DaemonRuntimeState{}, false, fmt.Errorf("read daemon runtime: %w", err)
	}
	if len(data) == 0 {
		return DaemonRuntimeState{}, false, nil
	}
	var state DaemonRuntimeState
	if err := json.Unmarshal(data, &state); err != nil {
		return DaemonRuntimeState{}, false, fmt.Errorf("parse daemon runtime: %w", err)
	}
	return state, true, nil
}

func RemoveDaemonRuntime(baseDir string) error {
	if err := os.Remove(DaemonRuntimePath(baseDir)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove daemon runtime: %w", err)
	}
	return nil
}
