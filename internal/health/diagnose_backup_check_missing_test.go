package health

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A backup outcome ping answered with HTTP 404 means the monitor has no such check:
// nothing there alerts about backups. BackupCheckMissing reads it from the text the
// Reporter records, so the test sends the ping through the Reporter and the record
// through RecordOutcomePing, as the daemon does.
func TestDiagnosisBackupCheckMissingOnHTTP404(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{
		{http.StatusNotFound, true},
		{http.StatusInternalServerError, false},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status) }))
		base := t.TempDir()
		now := time.Now()
		if err := RecordOutcomePing(base, "self", KindHeartbeat, now.Unix(), true, false, nil); err != nil {
			t.Fatal(err)
		}
		rep := NewReporter(Config{Client: srv.Client(), BackupURL: srv.URL + "/ping/backup"})
		pingErr := rep.RunFinished(context.Background(), "", 0, "")
		srv.Close()
		if pingErr == nil {
			t.Fatalf("HTTP %d: the ping must fail", tc.status)
		}
		if err := RecordOutcomePing(base, "self", KindRunFinished, now.Unix(), false, false, pingErr); err != nil {
			t.Fatal(err)
		}
		st, err := LoadStatus(base)
		if err != nil {
			t.Fatal(err)
		}
		d := Diagnose(st, 5*time.Minute, now)
		if d.State != TxTransmitFailed || d.BackupCheckMissing() != tc.want {
			t.Fatalf("HTTP %d: state=%s err=%q BackupCheckMissing=%v, want %v", tc.status, d.State, d.Err, d.BackupCheckMissing(), tc.want)
		}
	}
}
