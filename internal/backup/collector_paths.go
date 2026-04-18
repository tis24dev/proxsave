package backup

import (
	"fmt"
	"path/filepath"
)

func (c *Collector) proxsaveInfoRoot() string {
	return filepath.Join(c.tempDir, "var/lib/proxsave-info")
}

func (c *Collector) proxsaveInfoDir(parts ...string) string {
	args := make([]string, 0, 1+len(parts))
	args = append(args, c.proxsaveInfoRoot())
	args = append(args, parts...)
	return filepath.Join(args...)
}

func (c *Collector) proxsaveCommandsDir(component string) string {
	return c.proxsaveInfoDir("commands", component)
}

func (c *Collector) proxsaveRuntimeDir(component string) string {
	return c.proxsaveInfoDir("runtime", component)
}

func (c *Collector) ensureCommandsDir(component string) (string, error) {
	dir := c.proxsaveCommandsDir(component)
	if err := c.ensureDir(dir); err != nil {
		return "", fmt.Errorf("failed to create commands directory: %w", err)
	}
	return dir, nil
}
