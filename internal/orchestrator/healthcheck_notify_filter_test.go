package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/notifyfilter"
)

// notifyFilterEnv writes backup.env, keeps the shell out of it, and stubs the relay read.
func notifyFilterEnv(t *testing.T, body string, st health.DeliveryStatus, err error) (string, string, *int) {
	t.Helper()
	for _, key := range []string{"NOTIFY_ON", "TELEGRAM_ENABLED", "TELEGRAM_ENABLE", "EMAIL_ENABLED", "EMAIL_ENABLE",
		"GOTIFY_ENABLED", "GOTIFY_ENABLE", "WEBHOOK_ENABLED", "WEBHOOK_ENABLE"} {
		t.Setenv(key, "")
	}
	base := t.TempDir()
	path := filepath.Join(base, "backup.env")
	if werr := os.WriteFile(path, []byte(body), 0o600); werr != nil {
		t.Fatal(werr)
	}
	reads := 0
	orig := notifyfilter.FetchDeliveryStatus
	t.Cleanup(func() { notifyfilter.FetchDeliveryStatus = orig })
	notifyfilter.FetchDeliveryStatus = func(_ context.Context, _ *http.Client, host, id, _ string) (health.DeliveryStatus, error) {
		reads++
		if host != testAPIHost || id != testServerID {
			t.Errorf("relay read with host %q id %q; want the ones the check resolved", host, id)
		}
		return st, err
	}
	return path, base, &reads
}

const (
	testAPIHost  = "https://relay.invalid"
	testServerID = "123456789012"
)

var upDaemon = health.Diagnosis{State: health.TxTransmitting, DaemonUp: true}

func readyFor(requested string, channels ...string) health.DeliveryStatus {
	return health.DeliveryStatus{SchemaVersion: 1, State: "ready", ValidForSeconds: 120,
		NotifyPolicy: &health.NotifyPolicyAck{ContractVersion: 1, Requested: requested, Applied: true, Channels: channels}}
}

const centralizedEnv = "HEALTHCHECK_ENABLED=true\nHEALTHCHECK_MODE=centralized\nTELEGRAM_ENABLED=true\n"

// The screen takes the run's decision from backup.env as it is now.
func TestCheckHealthcheckNotifyFilterTakesTheRunDecision(t *testing.T) {
	path, base, reads := notifyFilterEnv(t, centralizedEnv+"NOTIFY_ON=failure\n", readyFor("failure", "telegram"), nil)
	nf := CheckHealthcheckNotifyFilter(context.Background(), path, base, testAPIHost, testServerID, upDaemon)
	if !nf.Loaded || nf.Self || nf.Invalid || nf.FromDefault {
		t.Fatalf("flags = %+v", nf)
	}
	if nf.Requested != "failure" || nf.Effective != "failure" || nf.Status != notifyfilter.StatusReady || nf.Reason != "" || *reads != 1 {
		t.Fatalf("decision = %+v after %d reads; want failure applied on ready", nf.Decision, *reads)
	}
}

// The daemon diagnosis of the same Check decides "not transmitting", without asking the relay.
func TestCheckHealthcheckNotifyFilterUsesTheCheckDaemonDiagnosis(t *testing.T) {
	path, base, reads := notifyFilterEnv(t, centralizedEnv+"NOTIFY_ON=failure\n", readyFor("failure", "telegram"), nil)
	nf := CheckHealthcheckNotifyFilter(context.Background(), path, base, testAPIHost, testServerID, health.Diagnosis{State: health.TxNotActive})
	if nf.Effective != config.NotifyOnAlways || nf.Reason != notifyfilter.ReasonNotTransmitting || *reads != 0 {
		t.Fatalf("decision = %+v after %d reads; want always, not_transmitting, no relay read", nf.Decision, *reads)
	}
}

func TestCheckHealthcheckNotifyFilterSettingFlags(t *testing.T) {
	path, base, _ := notifyFilterEnv(t, centralizedEnv+"NOTIFY_ON=warnig\n", readyFor("always", "telegram"), nil)
	if nf := CheckHealthcheckNotifyFilter(context.Background(), path, base, testAPIHost, testServerID, upDaemon); !nf.Invalid || nf.Raw != "warnig" || nf.Requested != config.NotifyOnAlways {
		t.Fatalf("invalid value: %+v", nf)
	}
	path, base, _ = notifyFilterEnv(t, centralizedEnv, readyFor("warning", "telegram"), nil)
	if nf := CheckHealthcheckNotifyFilter(context.Background(), path, base, testAPIHost, testServerID, upDaemon); !nf.FromDefault || nf.Requested != config.NotifyOnWarning {
		t.Fatalf("absent variable: %+v", nf)
	}
	path, base, reads := notifyFilterEnv(t, "HEALTHCHECK_ENABLED=true\nHEALTHCHECK_MODE=self\nNOTIFY_ON=failure\n", health.DeliveryStatus{}, errors.New("unused"))
	if nf := CheckHealthcheckNotifyFilter(context.Background(), path, base, testAPIHost, testServerID, upDaemon); !nf.Loaded || !nf.Self || *reads != 0 {
		t.Fatalf("self mode: %+v after %d reads", nf, *reads)
	}
	if nf := CheckHealthcheckNotifyFilter(context.Background(), filepath.Join(base, "missing.env"), base, testAPIHost, testServerID, upDaemon); nf.Loaded {
		t.Fatalf("unreadable backup.env: %+v", nf)
	}
}

// Healthchecks switched off in backup.env: the screen shows what the run applies, always, without
// asking the relay.
func TestCheckHealthcheckNotifyFilterHealthchecksOffInTheFile(t *testing.T) {
	path, base, reads := notifyFilterEnv(t, "HEALTHCHECK_ENABLED=false\nHEALTHCHECK_MODE=centralized\nNOTIFY_ON=failure\n", readyFor("failure"), nil)
	nf := CheckHealthcheckNotifyFilter(context.Background(), path, base, testAPIHost, testServerID, upDaemon)
	if !nf.Loaded || nf.Effective != config.NotifyOnAlways || *reads != 0 {
		t.Fatalf("decision = %+v after %d reads; want always and no relay read", nf, *reads)
	}
	if setting, current, ok := HealthcheckNotifyLines(false, nf); !ok || setting != "NOTIFY_ON=failure" || current != "always" {
		t.Fatalf("lines = %q / %q (%v)", setting, current, ok)
	}
}
