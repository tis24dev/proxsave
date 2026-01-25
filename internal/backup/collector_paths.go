package backup

import "path/filepath"

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

