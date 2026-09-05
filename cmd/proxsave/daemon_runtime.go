package main

import (
	"os"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
)

var daemonRuntimeWrite = health.WriteDaemonRuntime

func daemonRuntimeScriptFromDiagnostic(in personalScriptDiagnostic) health.DaemonRuntimeScript {
	components := make([]health.DaemonRuntimePathComponent, 0, len(in.Components))
	for _, component := range in.Components {
		components = append(components, health.DaemonRuntimePathComponent{
			Path: component.Path,
			UID:  component.UID,
			Mode: uint32(component.Mode),
		})
	}
	return health.DaemonRuntimeScript{
		Path: in.Path, State: string(in.State), Reason: in.Reason, Components: components,
	}
}

func buildDaemonRuntimeState(d *daemon, startTS int64, scripts personalScriptsDiagnostics) health.DaemonRuntimeState {
	return health.DaemonRuntimeState{
		SchemaVersion: health.DaemonRuntimeSchemaVersion,
		PID:           os.Getpid(),
		StartTS:       startTS,
		ConfigPath:    d.configPath,
		DaemonUID:     os.Geteuid(),
		PersonalScripts: health.DaemonRuntimeScripts{
			Pre:  daemonRuntimeScriptFromDiagnostic(scripts.Pre),
			Post: daemonRuntimeScriptFromDiagnostic(scripts.Post),
		},
	}
}

func (d *daemon) publishDaemonRuntime(startTS int64, scripts personalScriptsDiagnostics) {
	state := buildDaemonRuntimeState(d, startTS, scripts)
	if err := daemonRuntimeWrite(d.cfg.BaseDir, state); err != nil {
		logging.Warning("daemon: runtime diagnostics unavailable because state publication failed: %v", err)
	}
}
