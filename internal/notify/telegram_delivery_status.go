package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

// deliveryPollConfig tunes the post-send delivery-status poll.
type deliveryPollConfig struct {
	Timeout      time.Duration // total budget across all attempts
	Interval     time.Duration // base gap between attempts (grows with backoff)
	InitialDelay time.Duration // wait before the first poll (delivery needs >=1s)
}

// deliveryStatus is the resolved outcome of the poll.
type deliveryStatus struct {
	State     string // "delivered" | "failed" | "pending" | "unknown"
	MessageID int64
	Reason    string
	Attempts  int
	Err       error
}

// notifyStatusResponse mirrors the GET /api/notify/status success body.
type notifyStatusResponse struct {
	State             string `json:"state"`
	Attempts          int    `json:"attempts"`
	Reason            string `json:"reason"`
	TelegramMessageID int64  `json:"telegram_message_id"`
}

// pollTelegramDeliveryStatus polls GET /api/notify/status until the notification
// leaves pending/sending (delivered or failed) or the time budget elapses. It NEVER
// fails the caller: a still-pending row at the deadline returns "pending" (the
// durable outbox keeps retrying), a definitive lookup miss/auth issue returns
// "unknown", and transient network/5xx errors are retried until the deadline and
// then reported as "pending" (accepted, delivery unconfirmed). Only X-Server-Auth is
// sent; the bot token never appears here.
func pollTelegramDeliveryStatus(ctx context.Context, client *http.Client, serverAPIHost, serverID, notifySecret, notifyID string, cfg deliveryPollConfig, logger *logging.Logger) deliveryStatus {
	if client == nil {
		client = http.DefaultClient
	}
	done := logging.DebugStart(logger, "telegram delivery poll", "notifyID=%q timeout=%s interval=%s", notifyID, cfg.Timeout, cfg.Interval)

	endpoint := strings.TrimRight(serverAPIHost, "/") + "/api/notify/status" +
		"?server_id=" + url.QueryEscape(serverID) + "&notify_id=" + url.QueryEscape(notifyID)

	interval := cfg.Interval
	if interval <= 0 {
		interval = time.Second
	}
	deadline := time.Now().Add(cfg.Timeout)

	// Initial wait (+ per-notification jitter derived from notifyID so a burst of
	// clients does not poll in lockstep). Deterministic -> test-stable.
	if !sleepCtx(ctx, cfg.InitialDelay+notifyJitter(notifyID)) {
		done(ctx.Err())
		return deliveryStatus{State: "unknown", Err: ctx.Err()}
	}

	attempt := 0
	result := deliveryStatus{State: "pending"}
	for {
		attempt++
		st, retryable := fetchDeliveryStatusOnce(ctx, client, endpoint, notifySecret, deadline)
		if logger != nil {
			logger.Debug("Telegram: delivery poll attempt %d -> state=%s", attempt, st.State)
		}
		switch st.State {
		case "delivered", "failed":
			done(nil)
			return st
		case "pending":
			result = st
		default: // "unknown"
			if !retryable {
				// Definitive: old server (no route), row absent, stale secret, or
				// relay disabled -> stop, nothing to gain by retrying.
				done(nil)
				return st
			}
			// Transient error: keep the accepted-but-unconfirmed "pending" result.
		}

		if !time.Now().Add(interval).Before(deadline) {
			break // next sleep would overrun the budget
		}
		if !sleepCtx(ctx, interval) {
			result.Err = ctx.Err()
			done(result.Err)
			return result
		}
		if interval < 3*time.Second { // gentle backoff, capped ~3s
			interval *= 2
			if interval > 3*time.Second {
				interval = 3 * time.Second
			}
		}
	}

	done(nil)
	if logger != nil {
		logger.Debug("Telegram: delivery poll still pending after %s; treating as queued", cfg.Timeout)
	}
	return result
}

// fetchDeliveryStatusOnce does ONE GET. Returns the parsed status and whether an
// "unknown" outcome is worth retrying (true for transient network/5xx, false for a
// definitive 404/401/403/503).
func fetchDeliveryStatusOnce(ctx context.Context, client *http.Client, endpoint, notifySecret string, deadline time.Time) (deliveryStatus, bool) {
	// Cap the per-request timeout at the remaining budget so a hung server cannot
	// overrun ConfirmTimeout by a whole 5s; keep a small floor so a near-expired
	// budget still allows one real attempt.
	reqTimeout := 5 * time.Second
	if rem := time.Until(deadline); rem > 0 && rem < reqTimeout {
		reqTimeout = rem
	}
	if reqTimeout < 250*time.Millisecond {
		reqTimeout = 250 * time.Millisecond
	}
	reqCtx, cancel := context.WithTimeout(ctx, reqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, "GET", endpoint, nil)
	if err != nil {
		return deliveryStatus{State: "unknown", Err: err}, false
	}
	req.Header.Set("X-Server-Auth", notifySecret)
	setProxsaveVersionHeader(req)

	resp, err := client.Do(req)
	if err != nil {
		return deliveryStatus{State: "unknown", Err: err}, true // transient -> retry
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == 200:
		var body notifyStatusResponse
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&body); err != nil {
			return deliveryStatus{State: "unknown", Err: err}, true
		}
		state := body.State
		if state != "delivered" && state != "failed" && state != "pending" {
			state = "pending"
		}
		return deliveryStatus{
			State:     state,
			MessageID: body.TelegramMessageID,
			Reason:    body.Reason,
			Attempts:  body.Attempts,
		}, false
	case resp.StatusCode == 404 || resp.StatusCode == 401 || resp.StatusCode == 403:
		// Old server without the route, row not found, or stale secret: definitive,
		// do not retry.
		return deliveryStatus{State: "unknown", Err: fmt.Errorf("status HTTP %d", resp.StatusCode)}, false
	default:
		// 5xx (incl. 503 overload / rolling-restart / relay killswitch) or anything
		// unexpected: transient, retry within the budget (the message is already
		// accepted on the durable outbox, so pending is the honest fallback).
		return deliveryStatus{State: "unknown", Err: fmt.Errorf("status HTTP %d", resp.StatusCode)}, true
	}
}

// sleepCtx sleeps for d, returning false if the context is cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// notifyJitter derives a small deterministic delay (0..499ms) from the notifyID so a
// midnight burst of clients desynchronizes its first poll without needing a RNG.
func notifyJitter(notifyID string) time.Duration {
	if notifyID == "" {
		return 0
	}
	sum := 0
	for i := 0; i < len(notifyID); i++ {
		sum += int(notifyID[i])
	}
	return time.Duration(sum%500) * time.Millisecond
}
