package utils

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// FileExists checks whether a file exists.
func FileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

// DirExists checks whether a directory exists.
func DirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// EnsureDir creates a directory if it doesn't exist.
func EnsureDir(path string) error {
	if DirExists(path) {
		return nil
	}
	return os.MkdirAll(path, 0755)
}

// ComputeSHA256 computes the SHA256 checksum of a file.
func ComputeSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("cannot open file: %w", err)
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("cannot compute hash: %w", err)
	}

	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// GetFileSize returns the file size in bytes.
func GetFileSize(filePath string) (int64, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		return 0, fmt.Errorf("cannot stat file: %w", err)
	}
	return info.Size(), nil
}

// AbsPath returns the absolute path of a file.
func AbsPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("cannot get absolute path: %w", err)
	}
	return absPath, nil
}

// IsAbsPath checks whether a path is absolute.
func IsAbsPath(path string) bool {
	return filepath.IsAbs(path)
}
