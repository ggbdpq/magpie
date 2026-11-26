package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// ocrOwnConfig is a config.json as `ocr config provider` writes one: a
// preset of the user's on a model of its own, and a setting beside it.
const ocrOwnConfig = `{
  "provider": "anthropic",
  "model": "claude-sonnet-4-6",
  "language": "zh",
  "providers": {
    "anthropic": {"api_key": "sk-ant-own", "model": "claude-sonnet-4-6"}
  }
}
`

// Picking a model of magpie's for Open Code Review writes the custom
// provider "magpie" (openai protocol, the gateway's /v1, a key naming
// it, the model) and points provider at it; going back puts the user's
// own provider and model back and takes magpie's entry out.
func TestOCRWiring(t *testing.T) {
	syncHome(t)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1", "m2"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find(ocrID)
	if err != nil {
		t.Fatal(err)
	}
	path := a.Path
	if filepath.Base(filepath.Dir(path)) != ".opencodereview" || filepath.Base(path) != "config.json" {
		t.Fatalf("path %s", path)
	}
	hanakoWrite(t, path, ocrOwnConfig, 0o600)
	if !a.Detected() {
		t.Fatal("~/.opencodereview doesn't count as Open Code Review there")
	}
	if v := a.Field("model").Get(); v != "anthropic/claude-sonnet-4-6" {
		t.Fatalf("own model reads %q", v)
	}
	if a.Wired() {
		t.Fatal("the user's own anthropic reads as magpie's")
	}
	opts := a.Field("model").Options(a.Values())
	if !slices.ContainsFunc(opts, func(o Option) bool { return o.Value == "anthropic/claude-sonnet-4-6" }) ||
		!slices.ContainsFunc(opts, func(o Option) bool { return o.Value == "magpie/fake/m1" }) {
		t.Fatalf("options: %+v", opts)
	}

	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"provider":                         "magpie",
		"custom_providers.magpie.protocol": "openai",
		"custom_providers.magpie.url":      gatewayV1(),
		"custom_providers.magpie.api_key":  gateway.TokenFor(ocrID),
		"custom_providers.magpie.model":    "fake/m1",
		"language":                         "zh",
		"providers.anthropic.api_key":      "sk-ant-own",
	} {
		if v, _ := edit.GetJSON(path, k); v != want {
			b, _ := os.ReadFile(path)
			t.Fatalf("%s = %q, want %q in:\n%s", k, v, want, b)
		}
	}
	if v := a.Field("model").Get(); v != "magpie/fake/m1" {
		t.Fatalf("model reads %q", v)
	}
	if !a.Wired() {
		t.Fatalf("not connected after a pick: %v", a.Values())
	}
	if d := a.Drift(); d != nil {
		t.Fatalf("drift right after a set: %+v", d)
	}
	// another of magpie's models: the stash keeps the user's, not magpie's
	if err := a.Apply("model", "magpie/fake/m2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := edit.GetJSON(path, "custom_providers.magpie.model"); v != "fake/m2" {
		t.Fatalf("model %q", v)
	}

	// the url changed by hand: magpie says so
	if err := edit.SetJSON(path, edit.KV{Path: "custom_providers.magpie.url", Value: "http://127.0.0.1:1/v1"}); err != nil {
		t.Fatal(err)
	}
	if d := a.Drift(); d == nil {
		t.Fatal("a url that no longer reaches the gateway reads as fine")
	}
	if err := a.Apply("model", "magpie/fake/m2"); err != nil {
		t.Fatal(err)
	}

	if err := a.Disconnect(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"provider": "anthropic",
		"model":    "claude-sonnet-4-6",
		"language": "zh",
	} {
		if v, _ := edit.GetJSON(path, k); v != want {
			b, _ := os.ReadFile(path)
			t.Fatalf("after Disconnect %s = %q, want %q in:\n%s", k, v, want, b)
		}
	}
	if _, ok := edit.GetJSONRaw(path, "custom_providers"); ok {
		b, _ := os.ReadFile(path)
		t.Fatalf("magpie's entry, or the custom_providers made for it, left behind:\n%s", b)
	}
	if a.Wired() {
		t.Fatal("still connected after Disconnect")
	}
}

// A model of the user's own picked while on magpie goes the way `ocr
// config model` writes it, and magpie's entry goes; a provider OCR hasn't
// set up is turned away rather than written as one it can't reach.
func TestOCROwnPick(t *testing.T) {
	syncHome(t)
	if err := provider.Save(provider.Provider{ID: "fake", Name: "Fake", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}}); err != nil {
		t.Fatal(err)
	}
	a, err := Find(ocrID)
	if err != nil {
		t.Fatal(err)
	}
	hanakoWrite(t, a.Path, ocrOwnConfig, 0o600)
	if err := a.Apply("model", "magpie/fake/m1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "anthropic/claude-opus-4-6"); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"provider":                  "anthropic",
		"model":                     "claude-opus-4-6",
		"providers.anthropic.model": "claude-opus-4-6",
	} {
		if v, _ := edit.GetJSON(a.Path, k); v != want {
			t.Fatalf("%s = %q, want %q", k, v, want)
		}
	}
	if _, ok := edit.GetJSONRaw(a.Path, "custom_providers.magpie"); ok {
		t.Fatal("magpie's entry left behind")
	}
	if err := a.Apply("model", "nowhere/x"); err == nil {
		t.Fatal("a provider OCR hasn't set up was written")
	}
}

// `ocr` names other programs too: only one under a folder named for Open
// Code Review is its.
func TestOCRNotOurs(t *testing.T) {
	for bin, ours := range map[string]bool{
		"/usr/local/lib/node_modules/@alibaba-group/open-code-review/bin/ocr": true,
		"/home/u/.opencodereview/bin/ocr":                                     true,
		"/usr/bin/ocr":                                                        false,
		"/opt/homebrew/bin/ocr":                                               false,
	} {
		if got := !ocrNotOurs(bin); got != ours {
			t.Errorf("%s: ours %v, want %v", bin, got, ours)
		}
	}
}

// #7043: on Windows, npm installs ocr.cmd (and nvm-windows installs ocr.exe
// beside node_modules/@alibaba-group/open-code-review) whose own path does not
// contain "open-code-review". Also, when an unrelated `ocr` appears earlier on
// PATH, Detected/BinPath must continue to the next candidate via FindTools.
func TestOCRWindowsShimAndNextCandidate(t *testing.T) {
	t.Run("npm-cmd-shim", func(t *testing.T) {
		home := syncHome(t)
		shim := filepath.Join(home, "AppData", "Roaming", "npm", "ocr.cmd")
		writeFile(t, shim, "@ECHO off\r\nnode \"%dp0%\\node_modules\\@alibaba-group\\open-code-review\\bin\\ocr.js\" %*\r\n")
		if ocrNotOurs(shim) {
			t.Fatalf("npm ocr.cmd shim rejected: %s", shim)
		}
		other := filepath.Join(home, "other", "ocr.cmd")
		writeFile(t, other, "@ECHO off\r\ntesseract %*\r\n")
		if !ocrNotOurs(other) {
			t.Fatalf("unrelated ocr.cmd accepted: %s", other)
		}
		if !ocrNotOurs(filepath.Join(home, "missing", "ocr.cmd")) {
			t.Fatal("missing ocr.cmd accepted")
		}
	})

	t.Run("nvm-windows-exe-shim", func(t *testing.T) {
		home := syncHome(t)
		nvmDir := filepath.Join(home, "nvm", ".nodejs")
		shim := filepath.Join(nvmDir, "ocr.exe")
		writeFile(t, shim, "MZ\x90\x00compiled-shim")
		if err := os.MkdirAll(filepath.Join(nvmDir, "node_modules", "@alibaba-group", "open-code-review"), 0o755); err != nil {
			t.Fatal(err)
		}
		if ocrNotOurs(shim) {
			t.Fatalf("nvm-windows ocr.exe shim beside package rejected: %s", shim)
		}
	})

	t.Run("next-candidate-after-foreign-ocr", func(t *testing.T) {
		home := syncHome(t)
		firstDir := filepath.Join(home, "first")
		secondDir := filepath.Join(home, "second", "open-code-review")
		name := "ocr"
		body := "#!/bin/sh\necho ok\n"
		if runtime.GOOS == "windows" {
			name = "ocr.cmd"
			body = "@ECHO off\r\necho ok\r\n"
		}
		firstBin := filepath.Join(firstDir, name)
		secondBin := filepath.Join(secondDir, name)
		if err := os.MkdirAll(firstDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(secondDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(firstBin, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(secondBin, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", firstDir+string(os.PathListSeparator)+secondDir)
		a := ocrIn(here(home))
		if !a.Detected() {
			t.Fatalf("Detected() = false when valid ocr is second on PATH (%s after %s)", secondBin, firstBin)
		}
		if got := a.BinPath(); got != secondBin {
			t.Fatalf("BinPath() = %q, want %q", got, secondBin)
		}
	})
}
