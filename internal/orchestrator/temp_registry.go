package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tis24dev/proxsave/internal/logging"
)

const (
	defaultRegistryEnvVar = "PROXMOX_TEMP_REGISTRY_PATH"
	defaultRegistryPath   = "/var/run/proxsave/temp-dirs.json"
	registryFallbackDir   = "proxsave"
)

type tempDirRecord struct {
	Path      string    `json:"path"`
	PID       int       `json:"pid"`
	CreatedAt time.Time `json:"created_at"`
}

// TempDirRegistry tracks temporary directories created by the orchestrator and
// can remove orphaned directories left behind by crashed processes.
type TempDirRegistry struct {
	registryPath string
	lockPath     string
	logger       *logging.Logger
	mu           sync.Mutex
}

// NewTempDirRegistry initializes a registry at the given path.
func NewTempDirRegistry(logger *logging.Logger, registryPath string) (*TempDirRegistry, error) {
	if registryPath == "" {
		return nil, fmt.Errorf("registry path cannot be empty")
	}

	dir := filepath.Dir(registryPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create registry directory: %w", err)
	}

	return &TempDirRegistry{
		registryPath: registryPath,
		lockPath:     registryPath + ".lock",
		logger:       logger,
	}, nil
}

// Register stores the temporary directory info for later cleanup.
func (r *TempDirRegistry) Register(dir string) error {
	return r.updateEntries(func(entries []tempDirRecord) ([]tempDirRecord, error) {
		filtered := make([]tempDirRecord, 0, len(entries)+1)
		for _, entry := range entries {
			if entry.Path != dir {
				filtered = append(filtered, entry)
			}
		}

		filtered = append(filtered, tempDirRecord{
			Path:      dir,
			PID:       os.Getpid(),
			CreatedAt: time.Now().UTC(),
		})
		return filtered, nil
	})
}

// Deregister removes the directory from the registry.
func (r *TempDirRegistry) Deregister(dir string) error {
	return r.updateEntries(func(entries []tempDirRecord) ([]tempDirRecord, error) {
		changed := false
		filtered := make([]tempDirRecord, 0, len(entries))
		for _, entry := range entries {
			if entry.Path == dir {
				changed = true
				continue
			}
			filtered = append(filtered, entry)
		}
		if !changed {
			return entries, nil
		}
		return filtered, nil
	})
}

// CleanupOrphaned removes entries whose processes are gone or directories are too old.
// Returns the number of directories successfully removed.
func (r *TempDirRegistry) CleanupOrphaned(maxAge time.Duration) (int, error) {
	now := time.Now().UTC()
	cleanedCount := 0
	err := r.withLock(func(entries []tempDirRecord) ([]tempDirRecord, error) {
		updated := make([]tempDirRecord, 0, len(entries))
		for _, entry := range entries {
			stale := now.Sub(entry.CreatedAt) > maxAge
			alive := processAlive(entry.PID)

			if stale || !alive {
				if r.logger != nil {
					r.logger.Debug("Cleaning orphaned temp dir %s (pid=%d)...", entry.Path, entry.PID)
				}
				if err := os.RemoveAll(entry.Path); err != nil {
					if r.logger != nil {
						r.logger.Warning("Failed to cleanup temp dir %s: %v", entry.Path, err)
					}
					updated = append(updated, entry)
					continue
				}
				if r.logger != nil {
					r.logger.Debug("Cleaned orphaned temp dir %s (pid=%d)", entry.Path, entry.PID)
				}
				cleanedCount++
				continue
			}

			updated = append(updated, entry)
		}
		return updated, nil
	})
	return cleanedCount, err
}

func (r *TempDirRegistry) updateEntries(mutator func([]tempDirRecord) ([]tempDirRecord, error)) error {
	return r.withLock(func(entries []tempDirRecord) ([]tempDirRecord, error) {
		newEntries, err := mutator(entries)
		if err != nil {
			return nil, err
		}
		return newEntries, nil
	})
}

func (r *TempDirRegistry) withLock(mutator func([]tempDirRecord) ([]tempDirRecord, error)) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	lockFile, err := os.OpenFile(r.lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open registry lock: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock registry: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	entries, err := r.loadEntries()
	if err != nil {
		return err
	}

	modifiedEntries, err := mutator(entries)
	if err != nil {
		return err
	}

	return r.saveEntries(modifiedEntries)
}

func (r *TempDirRegistry) loadEntries() ([]tempDirRecord, error) {
	data, err := os.ReadFile(r.registryPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []tempDirRecord{}, nil
		}
		return nil, fmt.Errorf("read registry: %w", err)
	}

	if len(data) == 0 {
		return []tempDirRecord{}, nil
	}

	var entries []tempDirRecord
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse registry: %w", err)
	}
	return entries, nil
}

func (r *TempDirRegistry) saveEntries(entries []tempDirRecord) error {
	content, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	tmpPath := r.registryPath + ".tmp"
	if err := os.WriteFile(tmpPath, content, 0o640); err != nil {
		return fmt.Errorf("write temp registry: %w", err)
	}
	return os.Rename(tmpPath, r.registryPath)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil
}

func resolveRegistryPath() string {
	if custom := os.Getenv(defaultRegistryEnvVar); strings.TrimSpace(custom) != "" {
		return custom
	}

	if err := os.MkdirAll(filepath.Dir(defaultRegistryPath), 0o755); err == nil {
		return defaultRegistryPath
	}

	fallback := filepath.Join(os.TempDir(), registryFallbackDir, "temp-dirs.json")
	_ = os.MkdirAll(filepath.Dir(fallback), 0o755)
	return fallback
}
