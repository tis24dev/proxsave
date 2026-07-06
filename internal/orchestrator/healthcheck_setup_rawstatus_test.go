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
	origLoad, origFetch := healthcheckSetupLoadStatus, healthcheckSetupFetch
	t.Cleanup(func() {
		healthcheckSetupLoadStatus = origLoad
		healthcheckSetupFetch = origFetch
	})

	want := health.Status{
		Records: map[string]*health.PingRecord{health.KindHeartbeat: {TS: 1000, OK: true}},
		Update:  &health.UpdateRecord{Ping: health.PingRecord{TS: 1000, OK: true}, Available: true, Latest: "9.9.9"},
	}
	healthcheckSetupLoadStatus = func(baseDir string) (health.Status, error) { return want, nil }
	// Fail the fetch so the function returns early AFTER the status load.
	healthcheckSetupFetch = func(ctx context.Context, client *http.Client, host, id, secret string, login bool) (health.CentralizedConfig, error) {
		return health.CentralizedConfig{}, errors.New("stub-fetch-down")
	}

	res := CheckHealthcheckConnection(context.Background(), "https://h", "sid", t.TempDir(), time.Minute)
	if !res.HaveStatus {
		t.Fatalf("HaveStatus must be true when the status file was readable")
	}
	if res.RawStatus.Record(health.KindHeartbeat) == nil || res.RawStatus.Update == nil || !res.RawStatus.Update.Available {
		t.Fatalf("RawStatus must carry the loaded snapshot, got %+v", res.RawStatus)
	}
	if res.Err == nil {
		t.Fatalf("stub fetch failure should surface as res.Err")
	}
}
