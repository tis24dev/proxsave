package orchestrator

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
)

// TestCheckHealthcheckConnectionPopulatesRawStatus: the check reuses the status snapshot
// it already loads for the diagnosis (no extra I/O) - RawStatus/HaveStatus are populated
// even when the subsequent reachability fetch fails, so the sensor list can render.
func TestCheckHealthcheckConnectionPopulatesRawStatus(t *testing.T) {
	origFetch := healthcheckSetupFetch
	t.Cleanup(func() { healthcheckSetupFetch = origFetch })

	// Seed a real status file (heartbeat + update) so the shared CheckDaemonState reads it:
	// HaveStatus is content-based now, so RawStatus must carry the loaded snapshot.
	base := t.TempDir()
	if err := health.RecordPing(base, "self", health.KindHeartbeat, 1000, true, nil); err != nil {
		t.Fatalf("seed heartbeat: %v", err)
	}
	if err := health.RecordUpdate(base, "self", 1000, true, "9.9.9", true, nil); err != nil {
		t.Fatalf("seed update: %v", err)
	}
	// Fail the fetch so the function returns early AFTER the status load.
	healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, login bool) (health.CentralizedConfig, error) {
		return health.CentralizedConfig{}, errors.New("stub-fetch-down")
	}

	res := CheckHealthcheckConnection(context.Background(), "https://h", "sid", base, time.Minute)
	if !res.HaveStatus {
		t.Fatalf("HaveStatus must be true when the status file has content")
	}
	if res.RawStatus.Record(health.KindHeartbeat) == nil || res.RawStatus.Update == nil || !res.RawStatus.Update.Available {
		t.Fatalf("RawStatus must carry the loaded snapshot, got %+v", res.RawStatus)
	}
	if res.Err == nil {
		t.Fatalf("stub fetch failure should surface as res.Err")
	}
}
