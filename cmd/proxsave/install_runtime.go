package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/orchestrator"
)

// installRuntimeResult keeps application history separate from current liveness.
// A poll cannot prove that an old process applied the saved configuration.
type installRuntimeResult struct {
	Mode                string
	ConfigLoaded        bool
	EntrypointVerified  bool
	UnitAttempted       bool
	UnitApplied         bool
	ActivationConfirmed bool
	RestartAttempted    bool
	Restart             RestartVerifyResult
	CronRemoval         cronRemovalOutcome
	Verification        RestartVerifyResult
	Verified            bool
	Err                 error
}

type installRuntimeOperations struct {
	loadConfig     func(string, string) (*config.Config, error)
	entrypoints    func(context.Context) error
	inspect        func(context.Context) (bool, health.DaemonPresence, error)
	install        func(context.Context) error
	snapshot       func(context.Context, *config.Config) RestartVerifyResult
	verify         func(context.Context, *config.Config) RestartVerifyResult
	residentConfig func(health.DaemonState, string) (string, bool)
	restart        func(context.Context, string, string, bool, time.Duration) RestartVerifyResult
	removeCron     func(context.Context) (cronRemovalOutcome, error)
	warnCron       func(context.Context) int
	cronPresent    func(context.Context) (bool, bool)
	configureCron  func(context.Context, string) error
}

type installRuntimePreparation struct {
	configPath             string
	baseDir                string
	canonicalPath          string
	execInfo               ExecInfo
	bootstrap              *logging.BootstrapLogger
	cronSchedule           string
	ops                    installRuntimeOperations
	cfg                    *config.Config
	result                 installRuntimeResult
	cronComplete           bool
	needsRestart           bool
	initialState           health.DaemonState
	logs                   []string
	applicationErrorLogged bool
}

func newInstallRuntimePreparation(configPath, baseDir string, execInfo ExecInfo, bootstrap *logging.BootstrapLogger) *installRuntimePreparation {
	p := &installRuntimePreparation{configPath: configPath, baseDir: baseDir, canonicalPath: daemonExecPath, execInfo: execInfo, bootstrap: bootstrap}
	p.ops = installRuntimeOperations{
		loadConfig:  config.LoadConfigWithBaseDir,
		entrypoints: func(ctx context.Context) error { return runPostInstallEntrypoints(ctx, execInfo, bootstrap) },
		inspect:     inspectInstallDaemon,
		install: func(ctx context.Context) error {
			return installDaemonService(ctx, daemonExecPath, configPath, bootstrap)
		},
		snapshot: snapshotInstallDaemon,
		verify: func(ctx context.Context, cfg *config.Config) RestartVerifyResult {
			return verifyDaemonAligned(ctx, cfg.BaseDir, cfg.HealthcheckHeartbeatInterval)
		},
		residentConfig: func(state health.DaemonState, base string) (string, bool) {
			runtime, _ := resolveDaemonRuntime(state, base)
			return runtime.ConfigPath, runtime.Availability == daemonRuntimeAvailable && strings.TrimSpace(runtime.ConfigPath) != ""
		},
		restart: restartAndVerifyDaemon,
		removeCron: func(ctx context.Context) (cronRemovalOutcome, error) {
			return removeCanonicalCronEntry(ctx, cronCorrectPaths(execInfo.ExecPath), bootstrap)
		},
		warnCron:    func(ctx context.Context) int { return warnIndirectProxsaveCronOnDaemonInstall(ctx, bootstrap) },
		cronPresent: canonicalCronLinePresent,
		configureCron: func(ctx context.Context, schedule string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			runPostInstallSymlinksAndCron(ctx, baseDir, execInfo, bootstrap, schedule, configPath)
			if err := ctx.Err(); err != nil {
				return err
			}
			if daemonUnitInstalled() {
				if err := removeDaemonService(ctx, bootstrap); err != nil {
					logging.Warning("daemon: a previous daemon unit could not be removed (possible double execution): %v", err)
				} else {
					logging.Info("Removed the previous daemon service; this host now uses the cron scheduler.")
				}
			}
			return ctx.Err()
		},
	}
	return p
}

// Prepare returns only cancellation. Operational errors belong to the runtime
// result so the monitoring screen can still diagnose the actual system.
func (p *installRuntimePreparation) Prepare(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.result.ConfigLoaded {
		cfg, err := p.ops.loadConfig(p.configPath, p.baseDir)
		if err != nil {
			p.result.Err = err
			return ctx.Err()
		}
		p.cfg = cfg
		p.result.ConfigLoaded = true
		p.result.Mode = cfg.SchedulerMode
		p.result.Err = nil
	}
	if p.result.Mode == "daemon" {
		reconcileSchedulerAfterInstall(ctx, p)
	}
	return ctx.Err()
}

func (p *installRuntimePreparation) Finalize(ctx context.Context) error {
	if err := p.Prepare(ctx); err != nil {
		return err
	}
	if !p.result.ConfigLoaded {
		return p.result.Err
	}
	if p.result.Mode != "daemon" {
		if !p.cronComplete {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := p.ops.configureCron(ctx, p.cronSchedule); err != nil {
				return err
			}
			p.cronComplete = true
		}
		return ctx.Err()
	}
	p.RefreshStatus(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	p.logOutcome()
	// No rollback or fallback can infer safety from an error or an inactive unit.
	// Existing confirmed scheduling may keep a partial installation nonfatal.
	if p.result.Verification.ProcessAlive || p.result.Verification.State.Active {
		return nil
	}
	if !p.result.CronRemoval.Verified {
		if present, verified := p.ops.cronPresent(ctx); present && verified {
			return nil
		}
	}
	if p.result.Err != nil {
		return p.result.Err
	}
	return errors.New("daemon service is not running")
}

func (p *installRuntimePreparation) RefreshStatus(ctx context.Context) RestartVerifyResult {
	if p.cfg != nil && p.result.Mode == "daemon" {
		p.result.Verification = p.ops.snapshot(ctx, p.cfg)
		p.result.Verified = true
	}
	return p.result.Verification
}

func verifyInstallExecutable(canonical, expected string) error {
	actual, err := os.Stat(canonical)
	if err != nil {
		return err
	}
	wanted, err := os.Stat(expected)
	if err != nil {
		return err
	}
	if !actual.Mode().IsRegular() || !wanted.Mode().IsRegular() || !os.SameFile(actual, wanted) {
		return fmt.Errorf("canonical daemon executable %q does not identify %q", canonical, expected)
	}
	return nil
}

// installRuntimeVerdict gives configuration application priority over a later
// binary probe. The same selector feeds the log and the existing Daemon field.
func installRuntimeVerdict(result installRuntimeResult) (orchestrator.HealthcheckSetupLevel, string, string) {
	if !result.UnitApplied && result.Err != nil {
		return orchestrator.HealthcheckSetupLevelWarn, result.Err.Error(), fmt.Sprintf("Failed to enable the daemon service: %v", result.Err)
	}
	if result.RestartAttempted {
		switch classifyRestartVerify(result.Restart) {
		case restartVerifyDeferredConfig, restartVerifyDeferredBackup:
			return orchestrator.HealthcheckSetupLevelWarn, "configuration not applied; manual restart required", "Daemon configuration not applied; manual restart required."
		case restartVerifyError:
			level, keyword, _ := restartVerifyStatus(result.Restart)
			return level, keyword, fmt.Sprintf("Daemon restart failed: %v (it may still run the old binary; restart it manually).", result.Restart.Err)
		}
	}
	level, keyword := installVerifyVerdict(result.Verification)
	if level == orchestrator.HealthcheckSetupLevelOk {
		return level, keyword, fmt.Sprintf("Daemon verified: %s.", keyword)
	}
	return level, keyword, fmt.Sprintf("Daemon %s.", keyword)
}

func (p *installRuntimePreparation) logOutcome() {
	if !p.result.UnitApplied && p.result.Err != nil {
		if p.applicationErrorLogged {
			return
		}
		p.applicationErrorLogged = true
	}
	level, _, line := installRuntimeVerdict(p.result)
	if level == orchestrator.HealthcheckSetupLevelOk {
		logging.Info("%s", line)
	} else {
		logging.Warning("%s", line)
	}
}

// inspectInstallDaemon does not confuse unreadable unit metadata with absence.
func inspectInstallDaemon(ctx context.Context) (bool, health.DaemonPresence, error) {
	_, err := os.Stat(daemonUnitPath)
	if err != nil && !os.IsNotExist(err) {
		return false, health.DaemonPresence{}, err
	}
	return err == nil, daemonPresenceProbe(ctx), nil
}

func snapshotInstallDaemon(ctx context.Context, cfg *config.Config) RestartVerifyResult {
	st := restartVerifyDaemonState(health.DaemonStateInput{BaseDir: cfg.BaseDir, HeartbeatInterval: cfg.HealthcheckHeartbeatInterval, Now: time.Now(), Presence: daemonPresenceProbe(ctx), ProcAlive: probeProxsaveDaemonAlive, ProcStale: procBinaryStaleProbe})
	return RestartVerifyResult{ProcessAlive: st.ProcessAlive, Aligned: st.Aligned, FreshInfo: st.HaveInfo, State: st}
}

// restartLock accepts only an unchanged resident config outside the rewritten
// install path. A pathname alone cannot identify the lock an already-running
// backup loaded before the wizard saved a new LOCK_PATH into that same file.
func (p *installRuntimePreparation) restartLock() (string, bool) {
	path, known := p.ops.residentConfig(p.initialState, p.cfg.BaseDir)
	if !known || p.initialState.StartTS <= 0 || os.Getenv("LOCK_PATH") != "" {
		return "", false
	}
	if !filepath.IsAbs(path) {
		return "", false
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != filepath.Clean(path) {
		return "", false
	}
	installed, err := os.Stat(p.configPath)
	if err != nil {
		return "", false
	}
	before, err := os.Stat(path)
	if err != nil || !before.Mode().IsRegular() || os.SameFile(installed, before) {
		return "", false
	}
	metadata, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}
	start := time.Unix(p.initialState.StartTS, 0)
	changed := time.Unix(metadata.Ctim.Sec, metadata.Ctim.Nsec)
	if !before.ModTime().Before(start) || !changed.Before(start) {
		return "", false
	}
	resident, err := p.ops.loadConfig(path, p.cfg.BaseDir)
	if err != nil {
		return "", false
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", false
	}
	afterMetadata, ok := after.Sys().(*syscall.Stat_t)
	if !ok || metadata.Ctim != afterMetadata.Ctim {
		return "", false
	}
	// Only BASE_DIR is resolved independently of the installer shell. Other
	// references may use environment overrides or fall back to its HOME, and
	// relative paths depend on the resident process's unknown working directory.
	raw, _ := resident.Get("LOCK_PATH")
	remainingReferences := strings.Count(raw, "$")
	safeReferences := true
	os.Expand(raw, func(name string) string {
		remainingReferences--
		if strings.ToUpper(strings.TrimSpace(name)) != "BASE_DIR" {
			safeReferences = false
		}
		return ""
	})
	if !safeReferences || remainingReferences != 0 {
		return "", false
	}
	lock, known := backupLockFilePath(resident, resident.BaseDir)
	if !known || !filepath.IsAbs(lock) {
		return "", false
	}
	return lock, true
}

// PrepareCaptured drains the capture before returning; replay never logs again.
func (p *installRuntimePreparation) PrepareCaptured(ctx context.Context) error {
	restore := captureRunOutput(p.bootstrap, func(line string) { p.logs = append(p.logs, line) })
	defer restore()
	return p.Prepare(ctx)
}

func (p *installRuntimePreparation) Replay(emit func(string)) {
	for _, line := range p.logs {
		emit(line)
	}
	p.logs = nil
}
