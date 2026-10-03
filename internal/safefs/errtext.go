package safefs

import (
	"errors"
	"io/fs"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
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
// A bounded operation that timed out reads "timed out after <N>s", the phrase the fact
// lines use for a dead or stale mount, in whole seconds (WholeSeconds).
//
// A wrapper that names the file itself carries the path too ("failed to create
// temporary file in /mnt/x: ..."): every ": "-separated part that holds an absolute path
// is dropped (dropPathParts), so
//
//	failed to create temporary file in /mnt/x: createtemp /mnt/x/.tmp-a: read-only file system  ->  read-only file system
func SystemErrorText(err error) string {
	return dropPathParts(systemErrorText(err))
}

func systemErrorText(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())

	var te *TimeoutError
	if errors.As(err, &te) && te != nil {
		cause := "timed out"
		if te.Timeout > 0 {
			cause = "timed out after " + WholeSeconds(te.Timeout)
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

// dropPathParts removes the ": "-separated parts of a cause that hold an absolute path
// (a field starting with "/"), keeping the order of the rest. When every part holds one,
// the last part is kept: it is the closest to the system error.
func dropPathParts(text string) string {
	if text == "" {
		return text
	}
	parts := strings.Split(text, ": ")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if !holdsAbsolutePath(part) {
			kept = append(kept, part)
		}
	}
	if len(kept) == 0 {
		return parts[len(parts)-1]
	}
	return strings.Join(kept, ": ")
}

func holdsAbsolutePath(part string) bool {
	for _, field := range strings.Fields(part) {
		if strings.HasPrefix(field, "/") {
			return true
		}
	}
	return false
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

// WholeSeconds renders a duration for a fact line in whole seconds, a fraction rounded
// up: "120s", never "2m0s"; 500ms reads "1s". Every timeout fact of the run uses it.
func WholeSeconds(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	return strconv.FormatInt(int64(math.Ceil(d.Seconds())), 10) + "s"
}
