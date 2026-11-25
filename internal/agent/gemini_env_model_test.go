package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/edit"
)

// GEMINI_MODEL left in ~/.gemini/.env wins over the model magpie picks:
// Gemini CLI resolves argv.model, then GEMINI_MODEL (which it loads from
// ~/.gemini/.env itself), then settings.json's model.name (#7699, the
// reporter's trace). Picking a model pins the same value into the .env,
// so the model the CLI starts with is the one the UI shows.
func TestGeminiModelPinnedInEnvFile(t *testing.T) {
	home, _ := codexHome(t, "", "")
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}},"model":{"name":"gemini-2.5-pro"}}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_MODEL=gemini-3.8-flash\n"), 0o600)
	g := gemini(home)
	if err := g.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	if m, _ := edit.GetEnvFile(envPath, "GEMINI_MODEL"); m != "fake/m1" {
		t.Fatalf("GEMINI_MODEL = %q in %s, want fake/m1:\n%s", m, envPath, readFile(envPath))
	}
}

// A model of the CLI's own takes magpie out of the .env whole, the
// user's own GEMINI_MODEL back: while magpie serves, the pick is pinned
// there; off it, the file is theirs again (the local shape of
// TestWSLGeminiAddressMoved's last leg).
func TestGeminiOwnPickPutsTheUsersEnvModelBack(t *testing.T) {
	home, _ := codexHome(t, "", "")
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_MODEL=gemini-3.8-flash\n"), 0o600)
	g := gemini(home)
	if err := g.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	if m, _ := edit.GetEnvFile(envPath, "GEMINI_MODEL"); m != "fake/m1" {
		t.Fatalf("GEMINI_MODEL = %q while connected, want fake/m1", m)
	}
	if err := g.Apply("model", "gemini-2.5-pro"); err != nil {
		t.Fatal(err)
	}
	if body := readFile(envPath); body != "GEMINI_MODEL=gemini-3.8-flash\n" {
		t.Fatalf(".env after the CLI's own model:\n%s", body)
	}
}

// Disconnecting puts the user's own GEMINI_MODEL back, as it does their
// key and base URL.
func TestGeminiDisconnectRestoresEnvModel(t *testing.T) {
	home, _ := codexHome(t, "", "")
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_MODEL=gemini-3.8-flash\nGEMINI_API_KEY=mine\n"), 0o600)
	g := gemini(home)
	if err := g.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Disconnect(); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	if m, _ := edit.GetEnvFile(envPath, "GEMINI_MODEL"); m != "gemini-3.8-flash" {
		t.Fatalf("GEMINI_MODEL = %q after disconnecting, want the user's gemini-3.8-flash back:\n%s", m, readFile(envPath))
	}
	if k, _ := edit.GetEnvFile(envPath, "GEMINI_API_KEY"); k != "mine" {
		t.Fatalf("GEMINI_API_KEY = %q after disconnecting", k)
	}
}

// The model's default takes GEMINI_MODEL with it, so nothing magpie left
// pins a model behind a cleared pick.
func TestGeminiModelUnsetTakesEnvModelOut(t *testing.T) {
	home, _ := codexHome(t, "", "")
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	g := gemini(home)
	if err := g.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if m, _ := edit.GetEnvFile(filepath.Join(dir, ".env"), "GEMINI_MODEL"); m != "" {
		t.Fatalf("GEMINI_MODEL left: %q", m)
	}
}

// The check names a GEMINI_MODEL the .env still carries from a connection
// made by a magpie before the pick was synced there: the drift the report
// came from, left in the .env of every user connected before the fix
// until they pick the model again.
func TestGeminiCheckNamesEnvModelDrift(t *testing.T) {
	home, _ := codexHome(t, "", "")
	dir := filepath.Join(home, ".gemini")
	os.MkdirAll(dir, 0o755)
	g := gemini(home)
	if err := g.Apply("model", "fake/m1"); err != nil {
		t.Fatal(err)
	}
	envPath := filepath.Join(dir, ".env")
	if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_MODEL", Value: "gemini-3.8-flash"}); err != nil {
		t.Fatal(err)
	}
	d := gemini(home).Check()
	if !strings.Contains(d, "GEMINI_MODEL") || !strings.Contains(d, "gemini-3.8-flash") {
		t.Fatalf("check says %q, want the .env's GEMINI_MODEL named", d)
	}
	// with the pick in the .env, nothing is off
	if err := edit.SetEnvFile(envPath, edit.KV{Path: "GEMINI_MODEL", Value: "fake/m1"}); err != nil {
		t.Fatal(err)
	}
	if d := gemini(home).Check(); d != "" {
		t.Fatalf("check says %q with the pick synced", d)
	}
}
