package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tis24dev/proxsave/internal/health"
	"github.com/tis24dev/proxsave/internal/logging"
)

// provisioningRelay is a fake /api/healthcheck/config shaped like the relay's provisioning: a
// poll whose frequency differs from the one stored provisions it under a per-server lock the
// relay does not wait on, which takes delay, and is acked applied; a poll that finds that lock
// taken gets 200 with the stored, stale ack. It also answers the ping URLs it hands out, and
// records the most config polls it ever had in flight at once.
type provisioningRelay struct {
	provision sync.Mutex // the per-server lock, only ever TryLock'd
	delay     time.Duration

	mu          sync.Mutex
	down        bool   // answer 503 HC_NOT_READY
	stored      string // the frequency provisioned
	answered    int    // config polls answered while up
	inflight    int
	maxInflight int
	server      *httptest.Server
}

func newProvisioningRelay(t *testing.T, stored string, delay time.Duration) *provisioningRelay {
	t.Helper()
	r := &provisioningRelay{stored: stored, delay: delay, down: true}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.server.Close)
	return r
}

func (r *provisioningRelay) serve(w http.ResponseWriter, req *http.Request) {
	if strings.HasPrefix(req.URL.Path, "/ping/") {
		w.WriteHeader(http.StatusOK)
		return
	}
	r.mu.Lock()
	r.inflight++
	if r.inflight > r.maxInflight {
		r.maxInflight = r.inflight
	}
	down := r.down
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inflight--
		r.mu.Unlock()
	}()
	if down {
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "HC_NOT_READY"})
		return
	}

	r.mu.Lock()
	r.answered++
	r.mu.Unlock()
	q := req.URL.Query()
	want := q.Get("frequency")
	ack := want
	if r.provision.TryLock() {
		r.mu.Lock()
		delta := r.stored != want
		r.mu.Unlock()
		if delta {
			time.Sleep(r.delay)
			r.mu.Lock()
			r.stored = want
			r.mu.Unlock()
		}
		r.provision.Unlock()
	} else {
		r.mu.Lock()
		ack = r.stored
		r.mu.Unlock()
	}
	body := relayBody(q, true, true)
	body["alive_ping_url"] = "http://" + req.Host + "/ping/alive"
	body["backup_ping_url"] = "http://" + req.Host + "/ping/backup"
	week, grace := int64(7*86400), int64(3600)
	body["schedule"] = health.ScheduleAck{Requested: ack, Applied: true, Timeout: &week, Grace: &grace}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func (r *provisioningRelay) setDown(down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = down
}

// counts is the most config polls the relay had in flight at once, and how many it answered
// while up.
func (r *provisioningRelay) counts() (maxInflight, answered int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.maxInflight, r.answered
}

// After a start whose polls failed, with no ping URLs cached, the heartbeat's schedule retry and
// the update check's re-resolve fire on the same tick. The daemon sends their config polls one
// at a time, so the later one finds the frequency already provisioned and gets a fresh ack: the
// schedule is confirmed after that tick whichever poll goes first, and the relay never has two
// polls of the daemon in flight.
func TestConfigPollsOfOneTickAreSerialized(t *testing.T) {
	orig := daemonEvaluateUpdate
	t.Cleanup(func() { daemonEvaluateUpdate = orig })
	daemonEvaluateUpdate = func(context.Context, *logging.Logger, string) *UpdateInfo {
		return &UpdateInfo{Latest: "0.0.1"}
	}
	_ = captureDaemonLog(t)

	const runs = 100
	confirmed, overlapped := 0, 0
	for i := 0; i < runs; i++ {
		relay := newProvisioningRelay(t, "daily", 25*time.Millisecond)
		d := negotiationDaemon(t, relay.server.URL, "weekly", "mon", "1", "02:00")
		d.cfg.HealthcheckAliveURL, d.cfg.HealthcheckBackupURL = "", ""
		ctx := context.Background()

		if !d.logScheduleStart(ctx) {
			t.Fatalf("run %d: the start sent no poll", i)
		}
		d.mu.Lock()
		pending := !d.scheduleConfirmed
		d.mu.Unlock()
		if !pending || d.getReporter() != nil {
			t.Fatalf("run %d: after a failed start pending=%t reporter=%v; want pending and no reporter", i, pending, d.getReporter())
		}

		relay.setDown(false)
		tick := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); <-tick; d.beatTick(ctx) }()
		go func() { defer wg.Done(); <-tick; d.updateTick(ctx) }()
		close(tick)
		wg.Wait()

		maxInflight, answered := relay.counts()
		if answered != 2 {
			t.Fatalf("run %d: the tick sent %d config polls; want 2 (the heartbeat's retry and the update check's)", i, answered)
		}
		if maxInflight != 1 {
			overlapped++
		}
		d.mu.Lock()
		ok := d.scheduleConfirmed
		d.mu.Unlock()
		if ok {
			confirmed++
		}
	}
	if overlapped != 0 {
		t.Errorf("the relay had two config polls of the daemon in flight at once in %d of %d runs; want none", overlapped, runs)
	}
	if confirmed != runs {
		t.Errorf("schedule confirmed after the tick in %d of %d runs; want all", confirmed, runs)
	}
}
