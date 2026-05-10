package closeerr

import (
	"errors"
	"fmt"
	"io"
	"os"
)

// CloseIntoErr closes closer and stores the close failure in errp only when no
// earlier error is present.
func CloseIntoErr(errp *error, closer io.Closer, operation string) {
	if errp == nil || closer == nil {
		return
	}
	if closeErr := closer.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) && *errp == nil {
		*errp = fmt.Errorf("%s: %w", operation, closeErr)
	}
}
