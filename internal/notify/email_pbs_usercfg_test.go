package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// PBS writes user.cfg as section-config; the fallback read it as PVE's colon lines and
// never found root@pam's email (the bytes below are pve-test's PBS 4.2.5 file).
func TestPBSUserCfgFallbackReadsSectionConfig(t *testing.T) {
	notifier, err := NewEmailNotifier(EmailConfig{Enabled: true, DeliveryMethod: EmailDeliverySendmail}, types.ProxmoxBS, logging.New(types.LogLevelDebug, false))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	withEmail := filepath.Join(dir, "with-email.cfg")
	if err := os.WriteFile(withEmail, []byte("user: root@pam\n\tcomment Superuser\n\temail proxsave-probe@example.invalid\n\nuser: other@pbs\n\temail other@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if email, err := notifier.detectRecipientViaPBSUserCfg(withEmail, "root@pam"); err != nil || email != "proxsave-probe@example.invalid" {
		t.Fatalf("with email: %q, %v", email, err)
	}
	stock := filepath.Join(dir, "stock.cfg")
	if err := os.WriteFile(stock, []byte("user: root@pam\n\tcomment Superuser\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := notifier.detectRecipientViaPBSUserCfg(stock, "root@pam"); err == nil || !strings.Contains(err.Error(), "exists but has no email") {
		t.Fatalf("stock file: err %v, want the user without email", err)
	}
}
