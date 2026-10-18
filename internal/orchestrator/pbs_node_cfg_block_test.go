package orchestrator

import (
	"errors"
	"strings"
	"testing"
)

// pbsNodeRefused is the refusal proxmox-backup-manager 4.2.8 printed on VM 201
// (2026-10-18) for "node update --task-log-max-days=-5".
const pbsNodeRefused = "exit status 255: Error: parameter verification failed - 'task-log-max-days': value must have a minimum value of 0 (got -5)\nUsage: proxmox-backup-manager node update [OPTIONS]"

// The node configuration as one block, through the step runner: applied,
// refused (Clean writes the backup file, Merge does not), without the API, the
// fallback write refused, the staged file unreadable, absent, without settings.
func TestPBSNodeCfgBlock(t *testing.T) {
	const (
		target   = "/etc/proxmox-backup/node.cfg"
		opening  = "INFO     Restoring PBS node configuration..."
		outcome  = "PBS node configuration: "
		refusedC = "proxmox-backup-manager node update --task-log-max-days -5"
	)
	bad := "task-log-max-days: -5\n"
	mode := func(m string) string {
		return "DEBUG    PBS node restore: mode=" + m + " staged=/stage/etc/proxmox-backup/node.cfg"
	}
	refusal := []string{
		"DEBUG    PBS node restore: action=update keys=task-log-max-days",
		`DEBUG    PBS node restore: update failed error="exit status 255: Error: parameter verification failed - 'task-log-max-days': value must have a minimum value of 0 (got -5)\nUsage: proxmox-backup-manager node update [OPTIONS]"`,
		`INFO     The node configuration was rejected by PBS: "parameter verification failed - 'task-log-max-days': value must have a minimum value of 0 (got -5)"`,
	}
	// Clean mode also reads this PBS's node.cfg, absent here.
	refusalClean := append([]string{refusal[0], "DEBUG    PBS node restore: current keys="}, refusal[1:]...)
	fallback := "DEBUG    PBS node restore: fallback=file target=/etc/proxmox-backup/node.cfg source=/stage/etc/proxmox-backup/node.cfg"
	join := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	refuse := &FakeCommandRunner{Errors: map[string]error{refusedC: errors.New(pbsNodeRefused)}}
	cases := []struct {
		name     string
		behavior PBSRestoreBehavior
		staged   string
		runner   *FakeCommandRunner
		prepare  []func()
		want     []string
		warned   bool
		written  bool
	}{
		{name: "applied", behavior: PBSRestoreBehaviorMerge, staged: "email-from: pbs@example.com\n", runner: &FakeCommandRunner{},
			want: []string{opening, mode("merge"), "DEBUG    PBS node restore: action=update keys=email-from", "DEBUG    PBS node restore: updated", "INFO     ✓ PBS node configuration: applied"}},
		{name: "refused-clean", behavior: PBSRestoreBehaviorClean, staged: bad, runner: refuse,
			want:   join([]string{opening, mode("clean")}, refusalClean, []string{fallback, "DEBUG    PBS node restore: fallback written", "WARNING  ⚠ PBS node configuration: applied from the backup file"}),
			warned: true, written: true},
		{name: "refused-merge", behavior: PBSRestoreBehaviorMerge, staged: bad, runner: refuse,
			want:   join([]string{opening, mode("merge")}, refusal, []string{"WARNING  ⚠ PBS node configuration: not applied"}),
			warned: true},
		{name: "no-api-clean", behavior: PBSRestoreBehaviorClean, staged: bad, runner: &FakeCommandRunner{}, prepare: []func(){pbsAPIUnavailable},
			want:   []string{opening, mode("clean"), "INFO     The PBS API is not available", fallback, "DEBUG    PBS node restore: fallback written", "WARNING  ⚠ PBS node configuration: applied from the backup file"},
			warned: true, written: true},
		{name: "fallback-write-refused", behavior: PBSRestoreBehaviorClean, staged: bad, runner: refuse, prepare: []func(){func() {
			if err := restoreFS.MkdirAll(target+"/x", 0o755); err != nil {
				t.Fatal(err)
			}
		}},
			want: join([]string{opening, mode("clean"), refusal[0], `DEBUG    PBS node restore: current file=/etc/proxmox-backup/node.cfg read failed error="read <fakefs>/etc/proxmox-backup/node.cfg: is a directory"`}, refusal[1:], []string{fallback,
				`DEBUG    PBS node restore: fallback write failed error="write /etc/proxmox-backup/node.cfg: rename <fakefs>/etc/proxmox-backup/node.cfg.proxsave.tmp.<n> <fakefs>/etc/proxmox-backup/node.cfg: file exists"`,
				`INFO     The node configuration could not be written to this PBS: "write /etc/proxmox-backup/node.cfg: rename <fakefs>/etc/proxmox-backup/node.cfg.proxsave.tmp.<n> <fakefs>/etc/proxmox-backup/node.cfg: file exists"`,
				"WARNING  ⚠ PBS node configuration: not applied"}),
			warned: true},
		{name: "staged-unreadable", behavior: PBSRestoreBehaviorClean, staged: pbsStagedUnreadable, runner: &FakeCommandRunner{},
			want: []string{opening, mode("clean"),
				`DEBUG    PBS node restore: staged=/stage/etc/proxmox-backup/node.cfg read failed error="read staged etc/proxmox-backup/node.cfg: read <fakefs>/stage/etc/proxmox-backup/node.cfg: is a directory"`,
				`INFO     The node configuration in the backup could not be read: "read staged etc/proxmox-backup/node.cfg: read <fakefs>/stage/etc/proxmox-backup/node.cfg: is a directory"`,
				"WARNING  ⚠ PBS node configuration: not applied"},
			warned: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log, warned, fakeFS := runPBSSectionRestoreStep(t, tc.behavior, []string{"pbs_host"}, map[string]string{"etc/proxmox-backup/node.cfg": tc.staged}, tc.runner, tc.prepare...)
			got := pbsBlockFrom(t, log, opening, outcome)
			if strings.Join(got, "\n") != strings.Join(tc.want, "\n") || warned != tc.warned {
				t.Fatalf("warned=%v (want %v) block\n--- got ---\n%s\n--- want ---\n%s", warned, tc.warned, strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			for _, unwanted := range []string{"node config failed", "file-based node.cfg", "failed to apply", "PBS staged apply: etc/proxmox-backup/node.cfg"} {
				if strings.Contains(log, unwanted) {
					t.Fatalf("unexpected %q in log:\n%s", unwanted, log)
				}
			}
			if tc.name == "fallback-write-refused" {
				return
			}
			data, err := fakeFS.ReadFile(target)
			if tc.written != (err == nil) || (tc.written && string(data) != strings.TrimSpace(tc.staged)+"\n") {
				t.Fatalf("node.cfg %q (err %v), written want %v", data, err, tc.written)
			}
		})
	}
	for _, tc := range []struct{ name, staged, debug string }{
		{"absent", "", "DEBUG    PBS node restore: staged=/stage/etc/proxmox-backup/node.cfg not in the backup action=skip\n"},
		{"no-setting", "\n  \n", "DEBUG    PBS node restore: staged=/stage/etc/proxmox-backup/node.cfg has no setting action=skip\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{}
			if tc.name != "absent" {
				files["etc/proxmox-backup/node.cfg"] = tc.staged
			}
			runner := &FakeCommandRunner{}
			log, warned, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorClean, []string{"pbs_host"}, files, runner)
			if strings.Contains(log, opening) || warned || !strings.Contains(log, tc.debug) || len(runner.CallsList()) != 0 {
				t.Fatalf("expected DEBUG only, warned=%v calls=%v log:\n%s", warned, runner.CallsList(), log)
			}
		})
	}
	t.Run("proxy-credentials-never-logged", func(t *testing.T) {
		const staged = "http-proxy: http://user:pw@proxy.example:3128\n"
		runner := &FakeCommandRunner{Errors: map[string]error{"proxmox-backup-manager node update --http-proxy http://user:pw@proxy.example:3128": errors.New(pbsNodeRefused)}}
		log, _, _ := runPBSSectionRestoreStep(t, PBSRestoreBehaviorMerge, []string{"pbs_host"}, map[string]string{"etc/proxmox-backup/node.cfg": staged}, runner)
		if strings.Contains(log, "user:pw") {
			t.Fatalf("the proxy credentials leaked:\n%s", log)
		}
	})
}

// A Clean restore deletes, in the same node update, the settings this PBS has and
// the backup does not (key names at DEBUG, never values); Merge keeps them.
func TestPBSNodeCfgBlock_CleanDeletesSettingsTheBackupLacks(t *testing.T) {
	const current = "#old description\nemail-from: old@example.com\nhttp-proxy: http://user:pw@old.example:3128\ndefault-lang: de\nnot-a-setting: x\n"
	for _, tc := range []struct {
		name     string
		behavior PBSRestoreBehavior
		call     string
		debug    []string
	}{
		{"clean", PBSRestoreBehaviorClean,
			"proxmox-backup-manager node update --default-lang en --delete email-from --delete http-proxy --delete description",
			[]string{
				"DEBUG    PBS node restore: action=update keys=default-lang",
				"DEBUG    PBS node restore: current keys=email-from,http-proxy,default-lang,not-a-setting,description",
				"DEBUG    PBS node restore: action=delete keys=email-from,http-proxy,description",
				"DEBUG    PBS node restore: updated",
			}},
		{"merge", PBSRestoreBehaviorMerge, "proxmox-backup-manager node update --default-lang en",
			[]string{"DEBUG    PBS node restore: action=update keys=default-lang", "DEBUG    PBS node restore: updated"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &FakeCommandRunner{}
			log, warned, _ := runPBSSectionRestoreStep(t, tc.behavior, []string{"pbs_host"}, map[string]string{"etc/proxmox-backup/node.cfg": "default-lang: en\n"}, runner, func() {
				if err := restoreFS.WriteFile("/etc/proxmox-backup/node.cfg", []byte(current), 0o640); err != nil {
					t.Fatal(err)
				}
			})
			got := pbsBlockFrom(t, log, "INFO     Restoring PBS node configuration...", "PBS node configuration: ")
			want := append(append([]string{"INFO     Restoring PBS node configuration...", "DEBUG    PBS node restore: mode=" + tc.name + " staged=/stage/etc/proxmox-backup/node.cfg"}, tc.debug...), "INFO     ✓ PBS node configuration: applied")
			if strings.Join(got, "\n") != strings.Join(want, "\n") || warned {
				t.Fatalf("warned=%v block\n--- got ---\n%s\n--- want ---\n%s", warned, strings.Join(got, "\n"), strings.Join(want, "\n"))
			}
			if calls := runner.CallsList(); strings.Join(calls, "\n") != tc.call {
				t.Fatalf("calls %v, want %q", calls, tc.call)
			}
			for _, value := range []string{"old@example.com", "user:pw", "old description"} {
				if strings.Contains(log, value) {
					t.Fatalf("a value of this PBS's node.cfg reached the log: %q\n%s", value, log)
				}
			}
		})
	}
}
