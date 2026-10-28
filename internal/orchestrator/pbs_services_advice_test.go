package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tis24dev/proxsave/internal/config"
	"github.com/tis24dev/proxsave/internal/logging"
	"github.com/tis24dev/proxsave/internal/types"
)

// The closing advice said "PBS services - stopped and started again during this
// restore" whether or not the restart worked. A restart that leaves units down is now
// named in it, as the PVE one is.
func TestPBSRestartAdviceNamesTheUnitsLeftDown(t *testing.T) {
	orig := restoreCmd
	t.Cleanup(func() { restoreCmd = orig })
	restoreCmd = &FakeCommandRunner{Errors: map[string]error{"which systemctl": fmt.Errorf("not found")}}

	var buf bytes.Buffer
	logger := logging.New(types.LogLevelInfo, false)
	logger.SetOutput(&buf)
	w := &restoreUIWorkflowRun{
		ctx:                context.Background(),
		logger:             logger,
		cfg:                &config.Config{},
		plan:               &RestorePlan{SystemType: SystemTypePBS},
		systemType:         SystemTypePBS,
		pbsServicesStopped: true,
	}
	w.restartPBSServicesCleanup()()
	w.logPBSRestartAdvice()

	out := buf.String()
	if strings.Contains(out, "PBS services - stopped and started again") {
		t.Fatalf("the advice says the services were started again although the start failed:\n%s", out)
	}
	if !strings.Contains(out, "PBS services - stopped for this restore and not running: proxmox-backup, proxmox-backup-proxy (start failed)") {
		t.Fatalf("the advice does not name the units left down:\n%s", out)
	}
}
