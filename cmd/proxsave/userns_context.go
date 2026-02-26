package main

import (
	"strings"

	"github.com/tis24dev/proxsave/internal/environment"
	"github.com/tis24dev/proxsave/internal/logging"
)

func logUserNamespaceContext(logger *logging.Logger, info environment.UnprivilegedContainerInfo) {
	mode := "unknown"
	note := ""
	shifted := (info.UIDMap.OK && info.UIDMap.Outside0 != 0) || (info.GIDMap.OK && info.GIDMap.Outside0 != 0)
	container := strings.TrimSpace(info.ContainerRuntime)
	inContainer := container != ""
	nonRoot := info.EUID != 0

	switch {
	case shifted:
		mode = "unprivileged/rootless"
		note = " (some inventory may be skipped)"
	case nonRoot:
		mode = "non-root"
		note = " (some inventory may be skipped)"
	case inContainer:
		mode = "container"
		note = " (some inventory may be skipped)"
	case info.UIDMap.OK || info.GIDMap.OK:
		mode = "privileged"
	default:
		note = " (cannot infer; some inventory may be skipped)"
	}

	containerHint := ""
	if container != "" {
		containerHint = " (runtime=" + container + ")"
	}

	logging.Info("Privilege context: %s%s%s", mode, containerHint, note)

	done := logging.DebugStart(logger, "privilege context detection", "")
	logging.DebugStep(logger, "privilege context detection", "uid_map: path=%s ok=%v outside0=%d length=%d read_err=%q parse_err=%q evidence=%q",
		info.UIDMap.SourcePath, info.UIDMap.OK, info.UIDMap.Outside0, info.UIDMap.Length, info.UIDMap.ReadError, info.UIDMap.ParseError, info.UIDMap.RawEvidence)
	logging.DebugStep(logger, "privilege context detection", "gid_map: path=%s ok=%v outside0=%d length=%d read_err=%q parse_err=%q evidence=%q",
		info.GIDMap.SourcePath, info.GIDMap.OK, info.GIDMap.Outside0, info.GIDMap.Length, info.GIDMap.ReadError, info.GIDMap.ParseError, info.GIDMap.RawEvidence)
	logging.DebugStep(logger, "privilege context detection", "systemd container: path=%s ok=%v value=%q read_err=%q",
		info.SystemdContainer.Source, info.SystemdContainer.OK, strings.TrimSpace(info.SystemdContainer.Value), info.SystemdContainer.ReadError)
	logging.DebugStep(logger, "privilege context detection", "env container: key=%q set=%v value=%q",
		info.EnvContainer.Key, info.EnvContainer.Set, strings.TrimSpace(info.EnvContainer.Value))
	logging.DebugStep(logger, "privilege context detection", "marker: path=%s ok=%v present=%v stat_err=%q",
		info.DockerMarker.Source, info.DockerMarker.OK, info.DockerMarker.Present, info.DockerMarker.StatError)
	logging.DebugStep(logger, "privilege context detection", "marker: path=%s ok=%v present=%v stat_err=%q",
		info.PodmanMarker.Source, info.PodmanMarker.OK, info.PodmanMarker.Present, info.PodmanMarker.StatError)
	logging.DebugStep(logger, "privilege context detection", "cgroup hint: path=%s ok=%v value=%q read_err=%q",
		info.Proc1CgroupHint.Source, info.Proc1CgroupHint.OK, strings.TrimSpace(info.Proc1CgroupHint.Value), info.Proc1CgroupHint.ReadError)
	logging.DebugStep(logger, "privilege context detection", "cgroup hint: path=%s ok=%v value=%q read_err=%q",
		info.SelfCgroupHint.Source, info.SelfCgroupHint.OK, strings.TrimSpace(info.SelfCgroupHint.Value), info.SelfCgroupHint.ReadError)
	logging.DebugStep(logger, "privilege context detection", "computed: detected=%v shifted=%v euid=%d container=%q container_src=%q details=%q",
		info.Detected, shifted, info.EUID, container, strings.TrimSpace(info.ContainerSource), info.Details)
	done(nil)
}
