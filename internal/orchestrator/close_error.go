package orchestrator

import (
	"errors"
	"fmt"
	"io"
	"os"
)

func closeIntoErr(errp *error, closer io.Closer, operation string) {
	if errp == nil || closer == nil {
		return
	}
	if closeErr := closer.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) && *errp == nil {
		*errp = fmt.Errorf("%s: %w", operation, closeErr)
	}
}
