package orchestrator

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

type interceptRunner struct {
	calls []commandCall
}

type commandCall struct {
	name string
	args []string
}

func (r *interceptRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	_ = ctx
	r.calls = append(r.calls, commandCall{name: name, args: append([]string(nil), args...)})
	if name == "pvesh" && len(args) > 0 && args[0] == "set" {
		return nil, fmt.Errorf("not found")
	}
	return []byte("ok"), nil
}

func TestParseProxmoxNotificationSections(t *testing.T) {
	in := `
# comment
smtp: example
  mailto-user root@pam
  mailto-user admin@pve
  mailto max@example.com
  from-address pve1@example.com

matcher: default-matcher
  target example
  comment route
`

	sections, err := parseProxmoxNotificationSections(in)
	if err != nil {
		t.Fatalf("parseProxmoxNotificationSections error: %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(sections))
	}

	if sections[0].Type != "smtp" || sections[0].Name != "example" {
		t.Fatalf("unexpected first section: %#v", sections[0])
	}
	if len(sections[0].Entries) != 4 {
		t.Fatalf("expected 4 smtp entries, got %d", len(sections[0].Entries))
	}
	if sections[0].Entries[0].Key != "mailto-user" || sections[0].Entries[0].Value != "root@pam" {
		t.Fatalf("unexpected first smtp entry: %#v", sections[0].Entries[0])
	}

	if sections[1].Type != "matcher" || sections[1].Name != "default-matcher" {
		t.Fatalf("unexpected second section: %#v", sections[1])
	}
	if len(sections[1].Entries) != 2 {
		t.Fatalf("expected 2 matcher entries, got %d", len(sections[1].Entries))
	}
}

func TestApplyPVENotificationsFromStage_CreatesEndpointsAndMatchers(t *testing.T) {
	origFS := restoreFS
	origCmd := restoreCmd
	t.Cleanup(func() {
		restoreFS = origFS
		restoreCmd = origCmd
	})

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	runner := &interceptRunner{}
	restoreCmd = runner

	stageRoot := "/stage"
	cfg := `
smtp: example
  mailto-user root@pam
  mailto-user admin@pve
  mailto max@example.com
  from-address pve1@example.com
  username pve1
  server mail.example.com
  mode starttls
  comment hello

matcher: default-matcher
  target example
  comment route
`
	priv := `
smtp: example
  password somepassword
`

	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/notifications.cfg", []byte(cfg), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/pve/priv/notifications.cfg", []byte(priv), 0o600); err != nil {
		t.Fatalf("write staged priv notifications.cfg: %v", err)
	}

	if err := applyPVENotificationsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPVENotificationsFromStage error: %v", err)
	}

	want := []commandCall{
		{
			name: "pvesh",
			args: []string{
				"set", "/cluster/notifications/endpoints/smtp/example",
				"--mailto-user", "root@pam",
				"--mailto-user", "admin@pve",
				"--mailto", "max@example.com",
				"--from-address", "pve1@example.com",
				"--username", "pve1",
				"--server", "mail.example.com",
				"--mode", "starttls",
				"--comment", "hello",
				"--password", "somepassword",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"create", "/cluster/notifications/endpoints/smtp",
				"--name", "example",
				"--mailto-user", "root@pam",
				"--mailto-user", "admin@pve",
				"--mailto", "max@example.com",
				"--from-address", "pve1@example.com",
				"--username", "pve1",
				"--server", "mail.example.com",
				"--mode", "starttls",
				"--comment", "hello",
				"--password", "somepassword",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"set", "/cluster/notifications/matchers/default-matcher",
				"--target", "example",
				"--comment", "route",
			},
		},
		{
			name: "pvesh",
			args: []string{
				"create", "/cluster/notifications/matchers",
				"--name", "default-matcher",
				"--target", "example",
				"--comment", "route",
			},
		},
	}

	if len(runner.calls) != len(want) {
		t.Fatalf("calls=%d want %d: %#v", len(runner.calls), len(want), runner.calls)
	}
	for i := range want {
		if runner.calls[i].name != want[i].name {
			t.Fatalf("call[%d].name=%q want %q", i, runner.calls[i].name, want[i].name)
		}
		if fmt.Sprintf("%#v", runner.calls[i].args) != fmt.Sprintf("%#v", want[i].args) {
			t.Fatalf("call[%d].args=%#v want %#v", i, runner.calls[i].args, want[i].args)
		}
	}
}

func TestApplyPVEEndpointSection_RedactsSecretsInError(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	runner := &FakeCommandRunner{
		Errors: map[string]error{
			"pvesh set /cluster/notifications/endpoints/smtp/example --password somepassword":           fmt.Errorf("boom"),
			"pvesh create /cluster/notifications/endpoints/smtp --name example --password somepassword": fmt.Errorf("boom"),
		},
	}
	restoreCmd = runner

	section := proxmoxNotificationSection{
		Type:    "smtp",
		Name:    "example",
		Entries: []proxmoxNotificationEntry{{Key: "password", Value: "somepassword"}},
	}

	err := applyPVEEndpointSection(context.Background(), newTestLogger(), section)
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "somepassword") {
		t.Fatalf("expected password to be redacted from error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("expected <redacted> placeholder in error, got: %v", err)
	}
}

func TestApplyPBSNotificationsFromStage_WritesFilesWithPermissions(t *testing.T) {
	origFS := restoreFS
	t.Cleanup(func() { restoreFS = origFS })

	fakeFS := NewFakeFS()
	t.Cleanup(func() { _ = os.RemoveAll(fakeFS.Root) })
	restoreFS = fakeFS

	stageRoot := "/stage"
	// Real-world configs may include key/value lines before the first header (e.g. user edits).
	cfg := "comment Authenticated Gmail Relay, password: <redacted>\n\nsendmail: example\n  mailto-user root@pam\n"
	priv := "password <redacted>\nsendmail: example\n  secret token\n"

	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications.cfg", []byte(cfg), 0o640); err != nil {
		t.Fatalf("write staged notifications.cfg: %v", err)
	}
	if err := fakeFS.WriteFile(stageRoot+"/etc/proxmox-backup/notifications-priv.cfg", []byte(priv), 0o600); err != nil {
		t.Fatalf("write staged notifications-priv.cfg: %v", err)
	}

	if err := applyPBSNotificationsFromStage(context.Background(), newTestLogger(), stageRoot); err != nil {
		t.Fatalf("applyPBSNotificationsFromStage error: %v", err)
	}

	if _, err := fakeFS.ReadFile("/etc/proxmox-backup/notifications.cfg"); err != nil {
		t.Fatalf("expected restored notifications.cfg: %v", err)
	}
	if _, err := fakeFS.ReadFile("/etc/proxmox-backup/notifications-priv.cfg"); err != nil {
		t.Fatalf("expected restored notifications-priv.cfg: %v", err)
	}

	if info, err := fakeFS.Stat("/etc/proxmox-backup/notifications.cfg"); err != nil {
		t.Fatalf("stat notifications.cfg: %v", err)
	} else if info.Mode().Perm() != 0o640 {
		t.Fatalf("notifications.cfg mode=%#o want %#o", info.Mode().Perm(), 0o640)
	}
	if info, err := fakeFS.Stat("/etc/proxmox-backup/notifications-priv.cfg"); err != nil {
		t.Fatalf("stat notifications-priv.cfg: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("notifications-priv.cfg mode=%#o want %#o", info.Mode().Perm(), 0o600)
	}
}

func TestApplyPBSNotificationsViaProxmoxBackupManager_CreatesEndpointsAndMatchers(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	cfg := `
smtp: example
  mailto-user root@pam
  server mail.example.com
  from-address pbs1@example.com

matcher: default-matcher
  target example
  comment route
`
	priv := `
smtp: example
  password somepassword
`

	updateEndpoint := "proxmox-backup-manager notification endpoint smtp update example --mailto-user root@pam --server mail.example.com --from-address pbs1@example.com --password somepassword"
	updateMatcher := "proxmox-backup-manager notification matcher update default-matcher --target example --comment route"

	runner := &FakeCommandRunner{
		Errors: map[string]error{
			updateEndpoint: fmt.Errorf("not found"),
			updateMatcher:  fmt.Errorf("not found"),
		},
	}
	restoreCmd = runner

	if err := applyPBSNotificationsViaProxmoxBackupManager(context.Background(), newTestLogger(), cfg, priv); err != nil {
		t.Fatalf("applyPBSNotificationsViaProxmoxBackupManager error: %v", err)
	}

	want := []string{
		updateEndpoint,
		"proxmox-backup-manager notification endpoint smtp create example --mailto-user root@pam --server mail.example.com --from-address pbs1@example.com --password somepassword",
		updateMatcher,
		"proxmox-backup-manager notification matcher create default-matcher --target example --comment route",
	}

	if fmt.Sprintf("%#v", runner.Calls) != fmt.Sprintf("%#v", want) {
		t.Fatalf("calls=%#v want %#v", runner.Calls, want)
	}
}

func TestApplyPBSEndpointSection_RedactsSecretsInError(t *testing.T) {
	origCmd := restoreCmd
	t.Cleanup(func() { restoreCmd = origCmd })

	runner := &FakeCommandRunner{
		Errors: map[string]error{
			"proxmox-backup-manager notification endpoint smtp update example --password somepassword": fmt.Errorf("boom"),
			"proxmox-backup-manager notification endpoint smtp create example --password somepassword": fmt.Errorf("boom"),
		},
	}
	restoreCmd = runner

	section := proxmoxNotificationSection{
		Type:    "smtp",
		Name:    "example",
		Entries: []proxmoxNotificationEntry{{Key: "password", Value: "somepassword"}},
	}

	err := applyPBSEndpointSection(context.Background(), newTestLogger(), section)
	if err == nil {
		t.Fatalf("expected error")
	}
	if strings.Contains(err.Error(), "somepassword") {
		t.Fatalf("expected password to be redacted from error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "<redacted>") {
		t.Fatalf("expected <redacted> placeholder in error, got: %v", err)
	}
}
