package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// config is what `enrichednow login` saves.
type config struct {
	APIKey string `json:"api_key"`
}

// configPath is the file the key is saved in: enrichednow/config.json in the user's configuration directory
// (~/.config on Linux, ~/Library/Application Support on macOS, %AppData% on Windows), or in ENRICHEDNOW_CONFIG_DIR.
func configPath() (string, error) {
	if dir := os.Getenv(envConfigDir); dir != "" {
		return filepath.Join(dir, "config.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", &cliError{code: "config", msg: "cannot tell where to keep the configuration: " + err.Error(),
			hint: "Set " + envConfigDir + " to a directory, or use " + envAPIKey + ".", exit: exitError}
	}
	return filepath.Join(dir, "enrichednow", "config.json"), nil
}

func loadConfig() (config, error) {
	var cfg config
	path, err := configPath()
	if err != nil {
		return cfg, nil // no place for a configuration is the same as none saved
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, &cliError{code: "config", msg: fmt.Sprintf("cannot read %s: %v", path, err), exit: exitError}
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, &cliError{code: "config", msg: fmt.Sprintf("%s is not valid JSON: %v", path, err),
			hint: "Run `enrichednow login` to write it again.", exit: exitError}
	}
	return cfg, nil
}

// saveConfig writes the configuration so that only the user can read it: the directory 0700, the file 0600.
func saveConfig(cfg config) (string, error) {
	path, err := configPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", &cliError{code: "config", msg: err.Error(), exit: exitError}
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return "", &cliError{code: "config", msg: err.Error(), exit: exitError}
	}
	// WriteFile keeps the mode of a file that was already there; make sure of it.
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return "", &cliError{code: "config", msg: err.Error(), exit: exitError}
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", &cliError{code: "config", msg: err.Error(), exit: exitError}
	}
	return path, nil
}

// removeConfig deletes the saved configuration and reports whether there was one.
func removeConfig() (bool, error) {
	path, err := configPath()
	if err != nil {
		return false, err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, &cliError{code: "config", msg: err.Error(), exit: exitError}
	}
	return true, nil
}
