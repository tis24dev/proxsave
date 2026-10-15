package config

import "testing"

func withTemplate(t *testing.T, template string, fn func()) {
	t.Helper()
	orig := defaultEnvTemplate
	defaultEnvTemplate = template
	defer func() { defaultEnvTemplate = orig }()
	fn()
}

// LoadConfig reads the backup.env configuration file.
func LoadConfig(configPath string) (*Config, error) {
	return LoadConfigWithBaseDir(configPath, defaultBaseDir())
}

// UpgradeConfigFile merges the user's configuration with the embedded template.
func UpgradeConfigFile(configPath string) (*UpgradeResult, error) {
	return UpgradeConfigFileWithBaseDir(configPath, defaultBaseDir())
}
