package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
)

func logUserNamespaceContext(logger *logging.Logger, info environment.UnprivilegedContainerInfo) {
	mode := "unknown"
	note := ""
	switch {
	case info.Detected:
		mode = "unprivileged/rootless"
		note = " (some inventory may be skipped)"
	case info.UIDMap.OK || info.GIDMap.OK:
		mode = "privileged"
	default:
		note = " (cannot infer; some inventory may be skipped)"
	}

	container := ""
	if info.SystemdContainer.OK {
		container = strings.TrimSpace(info.SystemdContainer.Value)
	}
	containerHint := ""
	if container != "" {
		containerHint = " (container=" + container + ")"
	}

	logging.Info("Privilege context: %s%s%s", mode, containerHint, note)

	done := logging.DebugStart(logger, "privilege context detection", "")
	logging.DebugStep(logger, "privilege context detection", "uid_map: path=%s ok=%v outside0=%d length=%d read_err=%q parse_err=%q evidence=%q",
		info.UIDMap.SourcePath, info.UIDMap.OK, info.UIDMap.Outside0, info.UIDMap.Length, info.UIDMap.ReadError, info.UIDMap.ParseError, info.UIDMap.RawEvidence)
	logging.DebugStep(logger, "privilege context detection", "gid_map: path=%s ok=%v outside0=%d length=%d read_err=%q parse_err=%q evidence=%q",
		info.GIDMap.SourcePath, info.GIDMap.OK, info.GIDMap.Outside0, info.GIDMap.Length, info.GIDMap.ReadError, info.GIDMap.ParseError, info.GIDMap.RawEvidence)
	logging.DebugStep(logger, "privilege context detection", "systemd container: path=%s ok=%v value=%q read_err=%q",
		info.SystemdContainer.Source, info.SystemdContainer.OK, strings.TrimSpace(info.SystemdContainer.Value), info.SystemdContainer.ReadError)
	logging.DebugStep(logger, "privilege context detection", "computed: detected=%v details=%q", info.Detected, info.Details)
	done(nil)
}
