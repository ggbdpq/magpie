package appdir

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// DS Bot (github.com/Fei2411/DS-Bot) is a DSH plugin and an Electron
// desktop app built on it. The app ships its own DSH and runs it with
// DSH_HOME at a data folder of its own, booting the profile ds-bot, and
// never reads ~/.dsh. Its folder is chosen by chooseLocations
// (app/src/first-run.mjs): DS_BOT_HOME, which runs the app on throwaway
// folders, else locations.json's dataDir in the app's Electron userData
// (DS_BOT_SHELL_DATA when set, else <appData>/DS Bot, or <appData>/DeepSeek
// Bot, its name before, while that one is there and DS Bot's has no
// locations.json: configureIdentity in app/src/legacy.mjs), else ~/.ds-bot,
// the folder its first run saves when the user keeps the default.

// DsbotHome is DS Bot's data folder, the DSH_HOME its own DSH runs on, and
// whether it is known. A locations.json that can't be read or names no
// folder is unknown: never taken for the default, where an agent would be
// wired onto a folder the app doesn't run on.
func DsbotHome() (string, bool) {
	if dir := Getenv("DS_BOT_HOME"); dir != "" {
		return dir, true
	}
	userData := Getenv("DS_BOT_SHELL_DATA")
	if userData == "" {
		cfg, err := os.UserConfigDir()
		if err != nil {
			return "", false
		}
		userData = filepath.Join(cfg, "DS Bot")
		if legacy := filepath.Join(cfg, "DeepSeek Bot"); !isFile(filepath.Join(userData, "locations.json")) && isDir(legacy) {
			userData = legacy
		}
	}
	b, err := os.ReadFile(filepath.Join(userData, "locations.json"))
	if errors.Is(err, os.ErrNotExist) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		return filepath.Join(home, ".ds-bot"), true
	}
	if err != nil {
		return "", false
	}
	var places struct {
		DataDir string `json:"dataDir"`
	}
	if json.Unmarshal(b, &places) != nil || strings.TrimSpace(places.DataDir) == "" {
		return "", false
	}
	return places.DataDir, true
}

// DsbotOwns is whether dir is DS Bot's data folder: what its own DSH runs
// on, and no other agent's to bind, read or write. DS Bot's app exports
// DSH_HOME at that folder — a registry value on Windows, which Getenv
// follows while magpie runs — and DeepSeek Harness took it for its own
// home, writing its route over DS Bot's own (#5624).
func DsbotOwns(dir string) bool {
	if dir == "" {
		return false
	}
	home, ok := DsbotHome()
	return ok && sameDir(home) == sameDir(dir)
}

func isFile(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
func isDir(p string) bool  { st, err := os.Stat(p); return err == nil && st.IsDir() }
