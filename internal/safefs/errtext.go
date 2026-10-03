package safefs

import (
	"errors"
	"io/fs"
	"os"
	"strings"
)

// SystemErrorText renders the cause an operator-facing fact line shows after its label:
// the wording the code already wraps the failure in, followed by the system error
// WITHOUT the path. A fact line already names the file or the location it is about, so
// the path a *fs.PathError (or *os.LinkError) repeats only pushes the cause off the end
// of the line; the full chain belongs to DEBUG.
//
//	stat /mnt/backup/x.tar: no such file or directory  ->  no such file or directory
//	copy failed: open /mnt/x: permission denied         ->  copy failed: permission denied
//
// A bounded operation that timed out reads "timed out after <dur>", the phrase the fact
// lines use for a dead or stale mount.
func SystemErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())

	var te *TimeoutError
	if errors.As(err, &te) && te != nil {
		cause := "timed out"
		if te.Timeout > 0 {
			cause = "timed out after " + te.Timeout.String()
		}
		return replaceCause(text, te.Error(), cause)
	}

	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && pathErr != nil && pathErr.Err != nil {
		return replaceCause(text, pathErr.Error(), pathErr.Err.Error())
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) && linkErr != nil && linkErr.Err != nil {
		return replaceCause(text, linkErr.Error(), linkErr.Err.Error())
	}

	return text
}

// replaceCause swaps the path-carrying error's own text for its cause inside the full
// message. A wrapper that did not render that text verbatim leaves nothing to swap: the
// cause alone is then the only part known to carry no path.
func replaceCause(text, pathText, cause string) string {
	if pathText != "" && strings.Contains(text, pathText) {
		return strings.TrimSpace(strings.Replace(text, pathText, cause, 1))
	}
	return cause
}
