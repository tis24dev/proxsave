package orchestrator

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxPathSecuritySymlinkHops = 40

type pathResolutionErrorKind string

const (
	pathResolutionErrorSecurity    pathResolutionErrorKind = "security"
	pathResolutionErrorOperational pathResolutionErrorKind = "operational"
)

type pathResolutionError struct {
	kind  pathResolutionErrorKind
	msg   string
	cause error
}

func (e *pathResolutionError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause != nil {
		return fmt.Sprintf("%s: %v", e.msg, e.cause)
	}
	return e.msg
}

func (e *pathResolutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func newPathResolutionError(kind pathResolutionErrorKind, cause error, format string, args ...any) error {
	return &pathResolutionError{
		kind:  kind,
		msg:   fmt.Sprintf(format, args...),
		cause: cause,
	}
}

func newPathSecurityError(format string, args ...any) error {
	return newPathResolutionError(pathResolutionErrorSecurity, nil, format, args...)
}

func wrapPathOperationalError(cause error, format string, args ...any) error {
	return newPathResolutionError(pathResolutionErrorOperational, cause, format, args...)
}

func isPathSecurityError(err error) bool {
	var target *pathResolutionError
	return errors.As(err, &target) && target.kind == pathResolutionErrorSecurity
}

func isPathOperationalError(err error) bool {
	var target *pathResolutionError
	return errors.As(err, &target) && target.kind == pathResolutionErrorOperational
}

func resolvePathWithinRootFS(fsys FS, destRoot, candidate string) (string, error) {
	lexicalRoot, canonicalRoot, err := prepareRootPathsFS(fsys, destRoot)
	if err != nil {
		return "", err
	}

	candidateAbs, err := normalizeCandidateWithinRoot(lexicalRoot, canonicalRoot, candidate)
	if err != nil {
		return "", err
	}

	return resolvePathWithinPreparedRootFS(fsys, lexicalRoot, canonicalRoot, candidateAbs, true, maxPathSecuritySymlinkHops)
}

func resolvePathRelativeToBaseWithinRootFS(fsys FS, destRoot, baseDir, candidate string) (string, error) {
	lexicalRoot, canonicalRoot, err := prepareRootPathsFS(fsys, destRoot)
	if err != nil {
		return "", err
	}

	baseAbs, err := filepath.Abs(filepath.Clean(baseDir))
	if err != nil {
		return "", fmt.Errorf("resolve base directory: %w", err)
	}
	normalizedBaseDir, err := normalizeAbsolutePathWithinRoot(lexicalRoot, canonicalRoot, baseAbs)
	if err != nil {
		return "", err
	}

	// Resolve symlinks already present in the base directory before validating
	// a relative target against it. The kernel creates the new symlink under the
	// resolved parent directory, not the lexical parent path.
	canonicalBaseDir, err := resolvePathWithinPreparedRootFS(
		fsys,
		lexicalRoot,
		canonicalRoot,
		normalizedBaseDir,
		true,
		maxPathSecuritySymlinkHops,
	)
	if err != nil {
		return "", err
	}

	var candidateAbs string
	if filepath.IsAbs(candidate) {
		candidateAbs, err = normalizeAbsolutePathWithinRoot(lexicalRoot, canonicalRoot, filepath.Clean(candidate))
		if err != nil {
			return "", err
		}
	} else {
		candidateAbs = filepath.Clean(filepath.Join(canonicalBaseDir, candidate))
	}

	return resolvePathWithinPreparedRootFS(fsys, lexicalRoot, canonicalRoot, candidateAbs, true, maxPathSecuritySymlinkHops)
}

func prepareRootPathsFS(fsys FS, destRoot string) (string, string, error) {
	fsys = pathSecurityFS(fsys)

	cleanRoot := filepath.Clean(destRoot)
	absRoot, err := filepath.Abs(cleanRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve destination root: %w", err)
	}

	canonicalRoot, err := resolvePathFromFilesystemRootFS(fsys, absRoot, true, maxPathSecuritySymlinkHops)
	if err != nil {
		return "", "", fmt.Errorf("resolve destination root: %w", err)
	}

	return absRoot, canonicalRoot, nil
}

func normalizeCandidateWithinRoot(lexicalRoot, canonicalRoot, candidate string) (string, error) {
	if filepath.IsAbs(candidate) {
		return normalizeAbsolutePathWithinRoot(lexicalRoot, canonicalRoot, filepath.Clean(candidate))
	}

	return filepath.Clean(filepath.Join(canonicalRoot, candidate)), nil
}

func normalizeAbsolutePathWithinRoot(lexicalRoot, canonicalRoot, candidateAbs string) (string, error) {
	rel, ok, err := relativePathWithinRoot(lexicalRoot, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("cannot compute relative path: %w", err)
	}
	if ok {
		return filepath.Clean(filepath.Join(canonicalRoot, rel)), nil
	}

	rel, ok, err = relativePathWithinRoot(canonicalRoot, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("cannot compute relative path: %w", err)
	}
	if ok {
		return filepath.Clean(filepath.Join(canonicalRoot, rel)), nil
	}

	return "", newPathSecurityError("resolved path escapes destination: %s", candidateAbs)
}

func resolvePathFromFilesystemRootFS(fsys FS, candidateAbs string, allowMissingTail bool, hopsRemaining int) (string, error) {
	root := string(os.PathSeparator)
	return resolvePathWithinPreparedRootFS(fsys, root, root, candidateAbs, allowMissingTail, hopsRemaining)
}

func resolvePathWithinPreparedRootFS(fsys FS, lexicalRoot, canonicalRoot, candidateAbs string, allowMissingTail bool, hopsRemaining int) (string, error) {
	fsys = pathSecurityFS(fsys)

	lexicalRoot = filepath.Clean(lexicalRoot)
	canonicalRoot = filepath.Clean(canonicalRoot)
	candidateAbs = filepath.Clean(candidateAbs)

	if !filepath.IsAbs(lexicalRoot) {
		return "", fmt.Errorf("destination root must be absolute: %s", lexicalRoot)
	}
	if !filepath.IsAbs(canonicalRoot) {
		return "", fmt.Errorf("destination root must be absolute: %s", canonicalRoot)
	}
	if !filepath.IsAbs(candidateAbs) {
		return "", fmt.Errorf("candidate path must be absolute: %s", candidateAbs)
	}

	rel, ok, err := relativePathWithinRoot(canonicalRoot, candidateAbs)
	if err != nil {
		return "", fmt.Errorf("cannot compute relative path: %w", err)
	}
	if !ok {
		return "", newPathSecurityError("resolved path escapes destination: %s", candidateAbs)
	}
	if rel == "." {
		return canonicalRoot, nil
	}

	current := canonicalRoot
	parts := splitRelativePath(rel)
	for idx, part := range parts {
		next := filepath.Join(current, part)
		info, err := fsys.Lstat(next)
		if err != nil {
			if allowMissingTail && os.IsNotExist(err) {
				return filepath.Clean(filepath.Join(current, filepath.Join(parts[idx:]...))), nil
			}
			return "", wrapPathOperationalError(err, "lstat %s", next)
		}

		if info.Mode()&os.ModeSymlink != 0 {
			if hopsRemaining <= 0 {
				return "", newPathSecurityError("too many symlink resolutions for %s", candidateAbs)
			}
			target, err := fsys.Readlink(next)
			if err != nil {
				return "", wrapPathOperationalError(err, "readlink %s", next)
			}

			var resolvedLink string
			if filepath.IsAbs(target) {
				resolvedLink, err = normalizeAbsolutePathWithinRoot(lexicalRoot, canonicalRoot, filepath.Clean(target))
				if err != nil {
					return "", err
				}
			} else {
				resolvedLink = filepath.Join(current, target)
			}
			resolvedLink = filepath.Clean(resolvedLink)

			if _, ok, err := relativePathWithinRoot(canonicalRoot, resolvedLink); err != nil {
				return "", fmt.Errorf("cannot compute relative path: %w", err)
			} else if !ok {
				return "", newPathSecurityError("resolved path escapes destination: %s", resolvedLink)
			}

			remainder := filepath.Join(parts[idx+1:]...)
			if remainder != "" {
				resolvedLink = filepath.Join(resolvedLink, remainder)
			}

			return resolvePathWithinPreparedRootFS(fsys, lexicalRoot, canonicalRoot, resolvedLink, allowMissingTail, hopsRemaining-1)
		}

		if !info.IsDir() && idx < len(parts)-1 {
			return "", newPathResolutionError(pathResolutionErrorOperational, nil, "path component is not a directory: %s", next)
		}

		current = next
	}

	return current, nil
}

func relativePathWithinRoot(root, candidate string) (string, bool, error) {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", false, err
	}
	if rel == "." {
		return rel, true, nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return rel, false, nil
	}
	return rel, true, nil
}

func splitRelativePath(rel string) []string {
	if rel == "" || rel == "." {
		return nil
	}
	return strings.Split(rel, string(os.PathSeparator))
}

func pathSecurityFS(fsys FS) FS {
	if fsys == nil {
		return osFS{}
	}
	return fsys
}
