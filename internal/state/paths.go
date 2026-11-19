package state

import (
	"fmt"
	"path/filepath"
)

// Paths are the config file and the usage directory beside it.
type Paths struct {
	ConfigFile string
	UsageDir   string
}

// ResolvePaths chooses the config file and the usage directory.
// When configEnv is set it is the config file and the default path is not used.
// Otherwise the file is aigate/config.json under userConfigDir.
// The usage directory is usage/ in the config file's directory.
func ResolvePaths(configEnv, userConfigDir string) (Paths, error) {
	var file string
	if configEnv != "" {
		abs, err := filepath.Abs(configEnv)
		if err != nil {
			return Paths{}, err
		}
		file = abs
	} else {
		if userConfigDir == "" {
			return Paths{}, fmt.Errorf("user config directory is empty")
		}
		file = filepath.Join(userConfigDir, "aigate", "config.json")
	}
	return Paths{
		ConfigFile: file,
		UsageDir:   filepath.Join(filepath.Dir(file), "usage"),
	}, nil
}
