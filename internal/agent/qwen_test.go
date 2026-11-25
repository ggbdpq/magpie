package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// qwenUser is a settings.json as Qwen Code leaves it for someone with a
// custom model provider of their own, and settings magpie has nothing to
// do with.
const qwenUser = `{
  "env": {
    "IDEALAB_API_KEY": "team-key"
  },
  "modelProviders": {
    "openai": [
      {
        "id": "glm-5",
        "name": "[IdeaLab] glm-5",
        "baseUrl": "https://idealab.example.com/api/openai/v1",
        "envKey": "IDEALAB_API_KEY"
      }
    ]
  },
  "model": {
    "name": "glm-5",
    "baseUrl": "https://idealab.example.com/api/openai/v1"
  },
  "outputLanguage": "zh-CN"
}
`

// qwenPinVersion pins what the qwen CLI says its version is, what qwenOneX
// branches on: the qwen on this machine's PATH is none of the tests', so
// every test here says which one it is. A 0.x qwen is the default every
// test here had before 1.x existed.
func qwenPinVersion(t *testing.T, v string) {
	t.Helper()
	was := qwenCLIVersion
	qwenCLIVersion = func() string { return v }
	t.Cleanup(func() { qwenCLIVersion = was })
}

func qwenHome(t *testing.T) (home, path string) {
	t.Helper()
	qwenPinVersion(t, "0.28.0")
	home = syncHome(t)
	for _, p := range []provider.Provider{
		{ID: "deepseek", Name: "DeepSeek", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"pro", "flash"}},
		{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1", Models: []string{"claude-sonnet-5"}},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	path = filepath.Join(home, ".qwen", "settings.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	return home, path
}

// qwenFile is settings.json read back: the modelProviders.openai entries
// by id, their order, and the whole file.
func qwenFile(t *testing.T, path string) (map[string]map[string]any, []string, map[string]any) {
	t.Helper()
	var f map[string]any
	if err := json.Unmarshal([]byte(readFile(path)), &f); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	byID := map[string]map[string]any{}
	var order []string
	mp, _ := f["modelProviders"].(map[string]any)
	ms, _ := mp["openai"].([]any)
	for _, m := range ms {
		e := m.(map[string]any)
		id, _ := e["id"].(string)
		byID[id] = e
		order = append(order, id)
	}
	return byID, order, f
}

func TestQwen(t *testing.T) {
	home, path := qwenHome(t)
	os.WriteFile(path, []byte(qwenUser), 0o644)
	a, err := Find("qwen")
	if err != nil || a.Path != path {
		t.Fatalf("find: %v %v", a, err)
	}
	if !a.Detected() {
		t.Fatal("not detected with ~/.qwen")
	}
	if got := a.Values()["model"]; got != "glm-5" {
		t.Fatalf("model: %q", got)
	}
	// the picker: the user's own entries, then magpie's catalog
	opts := a.Field("model").Options(a.Values())
	if opts[0].Value != "glm-5" || opts[0].Label != "[IdeaLab] glm-5" || opts[0].Group != "Qwen Code" {
		t.Fatalf("own options: %+v", opts[:1])
	}
	var via bool
	for _, o := range opts {
		via = via || o.Value == "magpie/deepseek/pro"
	}
	if !via {
		t.Fatalf("no magpie/deepseek/pro in %+v", opts)
	}

	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	ms, order, f := qwenFile(t, path)
	if order[0] != "glm-5" {
		t.Fatalf("the user's entry moved: %v", order)
	}
	// the user's entry is as it was
	if !strings.Contains(readFile(path), qwenUser[strings.Index(qwenUser, "      {"):strings.Index(qwenUser, "      }\n")+7]) {
		t.Fatalf("user's entry rewritten:\n%s", readFile(path))
	}
	m := f["model"].(map[string]any)
	if m["name"] != "deepseek/pro" || m["baseUrl"] != gatewayV1() {
		t.Fatalf("model: %v", m)
	}
	env := f["env"].(map[string]any)
	if env["IDEALAB_API_KEY"] != "team-key" || env["MAGPIE_QWEN_API_KEY"] != gateway.Token {
		t.Fatalf("env: %v", env)
	}
	if auth := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; auth != "openai" {
		t.Fatalf("selectedType: %v", auth)
	}
	if f["outputLanguage"] != "zh-CN" {
		t.Fatalf("settings: %v", f)
	}
	for _, id := range []string{"deepseek/pro", "deepseek/flash", "anth/claude-sonnet-5"} {
		e := ms[id]
		if e == nil {
			t.Errorf("%s missing: %v", id, order)
			continue
		}
		if e["name"] != listLabels("qwen")[id] || e["baseUrl"] != gatewayV1() || e["envKey"] != "MAGPIE_QWEN_API_KEY" {
			t.Errorf("%s: %v", id, e)
		}
	}
	if got := a.Values()["model"]; got != "magpie/deepseek/pro" {
		t.Fatalf("model read back: %q", got)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}
	// another of magpie's: the stash keeps the user's own
	if err := a.Apply("model", "magpie/anth/claude-sonnet-5"); err != nil {
		t.Fatal(err)
	}
	// a provider added since joins the picker on Sync
	provider.Save(provider.Provider{ID: "later", Name: "Later", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if ms, _, _ = qwenFile(t, path); ms["later/m1"] == nil {
		t.Fatalf("sync: %v", ms)
	}
	// something else rewrote magpie's entry: its baseUrl is the one
	// before its id, as the fields are ordered
	cur := readFile(path)
	at := strings.Index(cur, `"id": "anth/claude-sonnet-5"`)
	base := strings.LastIndex(cur[:at], gatewayV1())
	if base < 0 {
		t.Fatal("no baseUrl before the entry's name:\n" + cur)
	}
	edited := cur[:base] + "http://127.0.0.1:9/v1" + cur[base+len(gatewayV1()):]
	os.WriteFile(path, []byte(edited), 0o644)
	if d := a.Check(); !strings.Contains(d, "baseUrl") {
		t.Fatalf("check after a moved baseUrl: %q, model %q", d, a.Values()["model"])
	}
	a.Sync()
	if d := a.Check(); d != "" {
		t.Fatalf("check after sync: %s", d)
	}

	// reset: the user's model back, magpie's entries and key gone, the
	// rest as it was
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); got != qwenUser {
		t.Fatalf("not restored:\n%s", got)
	}
	if got := a.Values()["model"]; got != "glm-5" {
		t.Fatalf("reset model: %q", got)
	}
	_ = home
}

// Picking one of the user's own models while on magpie's takes magpie out.
func TestQwenOwnModel(t *testing.T) {
	_, path := qwenHome(t)
	os.WriteFile(path, []byte(qwenUser), 0o644)
	a := qwen(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "glm-5"); err != nil {
		t.Fatal(err)
	}
	ms, order, f := qwenFile(t, path)
	if len(order) != 1 || ms["glm-5"] == nil {
		t.Fatalf("models: %v", order)
	}
	m := f["model"].(map[string]any)
	if m["name"] != "glm-5" || m["baseUrl"] != "https://idealab.example.com/api/openai/v1" {
		t.Fatalf("model: %v", m)
	}
	env := f["env"].(map[string]any)
	if _, ok := env["MAGPIE_QWEN_API_KEY"]; ok {
		t.Fatalf("key left: %v", env)
	}
	// and a reset after that leaves the user's choice, not the stash
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	a.Apply("model", "")
	if got := a.Values()["model"]; got != "glm-5" {
		t.Fatalf("reset: %q", got)
	}
}

// No settings.json at all: magpie adds only its own, and a reset leaves
// the file as empty as it began.
func TestQwenFresh(t *testing.T) {
	home, path := qwenHome(t)
	a := qwen(home)
	if a.Values()["model"] != "" {
		t.Fatal("model without a file")
	}
	if err := a.Apply("model", "magpie/deepseek/flash"); err != nil {
		t.Fatal(err)
	}
	if _, order, f := qwenFile(t, path); len(order) != 4 {
		t.Fatalf("models: %v", order)
	} else if env := f["env"].(map[string]any); env["MAGPIE_QWEN_API_KEY"] != gateway.Token {
		t.Fatalf("env: %v", env)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(readFile(path)); got != "{}" {
		t.Fatalf("left: %s", got)
	}
	// a file Qwen Code never put a modelProviders in reads as no models
	// of anyone at all, and magpie touches none of it
	os.WriteFile(path, []byte("{\n  \"fastModel\": \"qwen3.8-flash\"\n}\n"), 0o644)
	if got := a.Values()["model"]; got != "" {
		t.Fatalf("model without modelProviders: %q", got)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	if got := readFile(path); got != "{\n  \"fastModel\": \"qwen3.8-flash\"\n}\n" {
		t.Fatalf("nothing of magpie's to take out:\n%s", got)
	}
}

// A model the user picked without a matching entry keeps no baseUrl;
// one of the user's under magpie's own env key name, and their auth
// type, are stashed and put back when magpie steps out.
func TestQwenBareModelAndOwnEnvKey(t *testing.T) {
	_, path := qwenHome(t)
	bare := `{
  "env": {
    "MAGPIE_QWEN_API_KEY": "the-user's-own"
  },
  "security": {
    "auth": {
      "selectedType": "qwen-oauth"
    }
  },
  "model": {
    "name": "qwen3.8-max"
  }
}
`
	os.WriteFile(path, []byte(bare), 0o644)
	a := qwen(os.Getenv("HOME"))
	if got := a.Values()["model"]; got != "qwen3.8-max" {
		t.Fatalf("bare model: %q", got)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if _, _, f := qwenFile(t, path); f["env"].(map[string]any)["MAGPIE_QWEN_API_KEY"] != gateway.Token {
		t.Fatalf("key not magpie's: %v", f["env"])
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	json.Unmarshal([]byte(readFile(path)), &f)
	if f["env"].(map[string]any)["MAGPIE_QWEN_API_KEY"] != "the-user's-own" {
		t.Fatalf("key not restored: %v", f["env"])
	}
	if got := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; got != "qwen-oauth" {
		t.Fatalf("auth not restored: %v", got)
	}
	m, ok := f["model"].(map[string]any)
	if !ok || m["name"] != "qwen3.8-max" {
		t.Fatalf("model not restored: %v", m)
	}
	if _, ok = m["baseUrl"]; ok {
		t.Fatalf("baseUrl it never had: %v", m)
	}
}

// An auth type of "openai" the user set themselves is told from
// magpie's, and stays when magpie steps out.
func TestQwenAuthOpenAIOfTheUsersOwn(t *testing.T) {
	_, path := qwenHome(t)
	os.WriteFile(path, []byte(`{
  "security": {
    "auth": {
      "selectedType": "openai"
    }
  },
  "model": {
    "name": "qwen3.8-max"
  }
}
`), 0o644)
	a := qwen(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	json.Unmarshal([]byte(readFile(path)), &f)
	if got := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; got != "openai" {
		t.Fatalf("the user's own openai came off: %v", f)
	}
}

// $QWEN_HOME moves the config folder Qwen Code looks at.
func TestQwenHomeOverride(t *testing.T) {
	home, _ := qwenHome(t)
	other := filepath.Join(home, "elsewhere")
	t.Setenv("QWEN_HOME", other)
	a := qwen(home)
	if a.Dir != other || a.Path != filepath.Join(other, "settings.json") {
		t.Fatalf("override: %s %s", a.Dir, a.Path)
	}
}

// love1sbug on X: Names in agents' lists changed nothing in Qwen Code's
// /model, which shows an entry's name (the id beside it), as magpie named
// every entry magpie/<id>. The name follows the setting now, written again
// as it changes.
func TestQwenNamesFollowSuffixMode(t *testing.T) {
	_, path := qwenHome(t)
	writeFile(t, path, qwenUser)
	if err := provider.SetModelName("deepseek/flash", "Quick"); err != nil {
		t.Fatal(err)
	}
	a, err := Find("qwen")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	catalog.Changed = SyncCatalog
	t.Cleanup(func() { catalog.Changed = nil })
	for _, c := range []struct{ mode, pro, flash string }{
		{provider.SuffixOn, "pro · DeepSeek", "Quick · DeepSeek"},
		{provider.SuffixOwn, "pro · DeepSeek", "Quick"},
		{provider.SuffixOff, "pro", "Quick"},
		{provider.SuffixOn, "pro · DeepSeek", "Quick · DeepSeek"},
	} {
		if err := provider.SetSuffixMode(c.mode); err != nil {
			t.Fatal(err)
		}
		ms, _, _ := qwenFile(t, path)
		if ms["deepseek/pro"]["name"] != c.pro || ms["deepseek/flash"]["name"] != c.flash {
			t.Fatalf("%s: %v / %v", c.mode, ms["deepseek/pro"], ms["deepseek/flash"])
		}
		if ms["glm-5"]["name"] != "[IdeaLab] glm-5" {
			t.Fatalf("%s: the user's entry renamed: %v", c.mode, ms["glm-5"])
		}
	}
}

// qwenUser1x is a settings.json as Qwen Code 1.x leaves it for someone
// with a custom provider of their own: providers is 1.x's top-level form
// (0.x's schema has no such key, and 1.x's has no modelProviders), and
// the model is keyed provider/model.
const qwenUser1x = `{
  "providers": {
    "idealab": {
      "type": "openai-compatible",
      "baseUrl": "https://idealab.example.com/api/openai/v1",
      "apiKey": "${IDEALAB_API_KEY}",
      "models": [
        { "model": "glm-5", "displayName": "[IdeaLab] glm-5" }
      ]
    }
  },
  "model": {
    "name": "idealab/glm-5"
  },
  "outputLanguage": "zh-CN"
}
`

// qwen1xFile is the providers object read back from settings.json, with
// magpie's member taken out: what is left is the user's.
func qwen1xFile(t *testing.T, path string) (magpie map[string]any, rest map[string]any, f map[string]any) {
	t.Helper()
	if err := json.Unmarshal([]byte(readFile(path)), &f); err != nil {
		t.Fatalf("%v\n%s", err, readFile(path))
	}
	pv, _ := f["providers"].(map[string]any)
	magpie, _ = pv["magpie"].(map[string]any)
	delete(pv, "magpie")
	rest = pv
	return magpie, rest, f
}

// The report: Qwen Code 1.x (v1.0.0-preview.1) reads top-level providers
// and keys a model provider/model, while magpie wrote 0.x's
// modelProviders.openai and a bare wire id as model.name, so /model
// showed none of magpie's models. A 1.x qwen gets providers.magpie (the
// catalog as models[].model, the pick as the member's model, the key
// itself as its apiKey), model.name "magpie/<wire id>", and the auth
// type openai (1.x's provider-backed one); stepping out takes only
// magpie's back, and the user's own providers stay as they were.
func TestQwen1x(t *testing.T) {
	home, path := qwenHome(t)
	qwenPinVersion(t, "1.0.0-preview.1")
	os.WriteFile(path, []byte(qwenUser1x), 0o644)
	a, err := Find("qwen")
	if err != nil || a.Path != path {
		t.Fatalf("find: %v %v", a, err)
	}
	if got := a.Values()["model"]; got != "idealab/glm-5" {
		t.Fatalf("model: %q", got)
	}
	// the picker: the user's own provider models keyed provider/model,
	// then magpie's catalog
	opts := a.Field("model").Options(a.Values())
	if opts[0].Value != "idealab/glm-5" || opts[0].Label != "[IdeaLab] glm-5" || opts[0].Group != "Qwen Code" {
		t.Fatalf("own options: %+v", opts[:1])
	}
	var via bool
	for _, o := range opts {
		via = via || o.Value == "magpie/deepseek/pro"
	}
	if !via {
		t.Fatalf("no magpie/deepseek/pro in %+v", opts)
	}

	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	mp, rest, f := qwen1xFile(t, path)
	if mp == nil {
		t.Fatalf("no providers.magpie:\n%s", readFile(path))
	}
	if mp["baseUrl"] != gatewayV1() || mp["apiKey"] != gateway.Token {
		t.Fatalf("magpie provider: %v", mp)
	}
	if mp["model"] != "magpie/deepseek/pro" {
		t.Fatalf("magpie provider's model (the pick, keyed like model.name): %v", mp)
	}
	ms, _ := mp["models"].([]any)
	got := map[string]bool{}
	for _, m := range ms {
		e := m.(map[string]any)
		id, _ := e["model"].(string)
		got[id] = true
		if e["displayName"] != listLabels("qwen")[id] {
			t.Errorf("%s: displayName %v", id, e["displayName"])
		}
	}
	for _, id := range []string{"deepseek/pro", "deepseek/flash", "anth/claude-sonnet-5"} {
		if !got[id] {
			t.Errorf("%s missing from models: %v", id, ms)
		}
	}
	if _, ok := f["modelProviders"]; ok {
		t.Fatalf("0.x modelProviders written for a 1.x qwen:\n%s", readFile(path))
	}
	// the user's own provider stays, and only magpie's member joins it
	if len(rest) != 1 || rest["idealab"] == nil {
		t.Fatalf("the user's provider moved: %v", rest)
	}
	m := f["model"].(map[string]any)
	if m["name"] != "magpie/deepseek/pro" {
		t.Fatalf("model: %v", m)
	}
	if _, ok := m["baseUrl"]; ok {
		t.Fatalf("baseUrl written, which 1.x's model refuses: %v", m)
	}
	if auth := f["security"].(map[string]any)["auth"].(map[string]any)["selectedType"]; auth != "openai" {
		t.Fatalf("selectedType: %v", auth)
	}
	// the key lives in the member, not in ~/.qwen/.env: magpie writes
	// nothing there for a 1.x qwen
	if k, _ := edit.GetEnvFile(filepath.Join(home, ".qwen", ".env"), "MAGPIE_QWEN_API_KEY"); k != "" {
		t.Fatalf("key written to .env: %q", k)
	}
	if got := a.Values()["model"]; got != "magpie/deepseek/pro" {
		t.Fatalf("model read back: %q", got)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}

	// a provider added since joins on Sync
	provider.Save(provider.Provider{ID: "later", Name: "Later", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	mp, _, _ = qwen1xFile(t, path)
	ms, _ = mp["models"].([]any)
	var later bool
	for _, m := range ms {
		if m.(map[string]any)["model"] == "later/m1" {
			later = true
		}
	}
	if !later {
		t.Fatalf("sync: %v", ms)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check after sync: %s", d)
	}

	// reset: the user's model back, providers.magpie and the key gone, the
	// rest as it was (the user's provider re-indented to the file's style,
	// so the rest is compared as JSON, not bytes)
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	var now, want map[string]any
	json.Unmarshal([]byte(readFile(path)), &now)
	json.Unmarshal([]byte(qwenUser1x), &want)
	nb, _ := json.Marshal(now)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(nb, wb) {
		t.Fatalf("not restored:\n%s", readFile(path))
	}
	if k, _ := edit.GetEnvFile(filepath.Join(home, ".qwen", ".env"), "MAGPIE_QWEN_API_KEY"); k != "" {
		t.Fatalf("key left in .env")
	}
	if got := a.Values()["model"]; got != "idealab/glm-5" {
		t.Fatalf("reset model: %q", got)
	}
}

// The report's own setup: a 1.x qwen whose settings magpie's older self
// had wired 0.x style — magpie's entries in modelProviders.openai, the
// key in the env object, model.name a bare wire id with a baseUrl — none
// of which 1.x reads. Wiring it as 1.x takes those of magpie's out (the
// user's own entries stay), and stepping out leaves them out.
func TestQwen1xLegacyWiring(t *testing.T) {
	home, path := qwenHome(t)
	qwenPinVersion(t, "1.0.0-preview.1")
	os.WriteFile(path, []byte(fmt.Sprintf(`{
  "env": {
    "IDEALAB_API_KEY": "team-key",
    "MAGPIE_QWEN_API_KEY": "%s"
  },
  "modelProviders": {
    "openai": [
      {
        "id": "glm-5",
        "name": "[IdeaLab] glm-5",
        "baseUrl": "https://idealab.example.com/api/openai/v1",
        "envKey": "IDEALAB_API_KEY"
      },
      {
        "id": "deepseek/pro",
        "name": "pro · DeepSeek",
        "baseUrl": "%s",
        "envKey": "MAGPIE_QWEN_API_KEY",
        "generationConfig": { "contextWindowSize": 262144 }
      }
    ]
  },
  "model": {
    "name": "deepseek/pro",
    "baseUrl": "%s"
  }
}
`, gateway.Token, gatewayV1(), gatewayV1())), 0o644)
	a := qwen(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	mp, rest, f := qwen1xFile(t, path)
	if mp == nil || mp["baseUrl"] != gatewayV1() {
		t.Fatalf("magpie provider: %v", mp)
	}
	// the user's own is the 0.x entry of theirs (they never had a 1.x
	// provider), and it stays
	if len(rest) != 0 {
		t.Fatalf("unexpected members of providers: %v", rest)
	}
	// magpie's 0.x entry is gone, the user's stays
	mp0, _ := f["modelProviders"].(map[string]any)
	os0, _ := mp0["openai"].([]any)
	if len(os0) != 1 || os0[0].(map[string]any)["id"] != "glm-5" {
		t.Fatalf("0.x entries: %v", os0)
	}
	env := f["env"].(map[string]any)
	if env["MAGPIE_QWEN_API_KEY"] != nil || env["IDEALAB_API_KEY"] != "team-key" {
		t.Fatalf("env: %v", env)
	}
	m := f["model"].(map[string]any)
	if m["name"] != "magpie/deepseek/pro" {
		t.Fatalf("model: %v", m)
	}
	if _, ok := m["baseUrl"]; ok {
		t.Fatalf("0.x baseUrl left: %v", m)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}

	// reset: magpie's 0.x wiring stays out too, the user's own entry of
	// theirs stays, and the model they had is back (a bare wire id of
	// theirs keeps no baseUrl: 1.x reads none, so the pick it had under
	// 0.x cannot be told apart there anyway)
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	mp, rest, f = qwen1xFile(t, path)
	if mp != nil {
		t.Fatalf("magpie provider left: %v", mp)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected members of providers after reset: %v", rest)
	}
	mp0, _ = f["modelProviders"].(map[string]any)
	os0, _ = mp0["openai"].([]any)
	if len(os0) != 1 || os0[0].(map[string]any)["id"] != "glm-5" {
		t.Fatalf("0.x entries after reset: %v", os0)
	}
	env = f["env"].(map[string]any)
	if env["MAGPIE_QWEN_API_KEY"] != nil || env["IDEALAB_API_KEY"] != "team-key" {
		t.Fatalf("env after reset: %v", env)
	}
	m = f["model"].(map[string]any)
	if m["name"] != "deepseek/pro" {
		t.Fatalf("model after reset: %v", m)
	}
	if _, ok := m["baseUrl"]; ok {
		t.Fatalf("baseUrl brought back: %v", m)
	}
	if k, _ := edit.GetEnvFile(filepath.Join(home, ".qwen", ".env"), "MAGPIE_QWEN_API_KEY"); k != "" {
		t.Fatalf("key left in .env: %q", k)
	}
}

// A qwen whose version magpie cannot learn (not on the PATH it looks at)
// keeps the wiring it has: a settings.json with providers.magpie in it is
// 1.x's, and Sync keeps that form, instead of writing 0.x's
// modelProviders.openai over it.
func TestQwen1xUnknownVersion(t *testing.T) {
	home, path := qwenHome(t)
	qwenPinVersion(t, "")
	os.WriteFile(path, []byte(fmt.Sprintf(`{
  "providers": {
    "magpie": {
      "baseUrl": "%s",
      "apiKey": "%s",
      "models": [
        { "model": "deepseek/pro", "displayName": "stale" }
      ]
    }
  },
  "model": {
    "name": "magpie/deepseek/pro"
  }
}
`, gatewayV1(), gateway.Token)), 0o644)
	a := qwen(home)
	if got := a.Values()["model"]; got != "magpie/deepseek/pro" {
		t.Fatalf("model: %q", got)
	}
	provider.Save(provider.Provider{ID: "later", Name: "Later", Key: "k", Chat: "http://127.0.0.1:1/v1", Models: []string{"m1"}})
	if err := a.Sync(); err != nil {
		t.Fatal(err)
	}
	mp, _, f := qwen1xFile(t, path)
	if mp == nil || mp["baseUrl"] != gatewayV1() {
		t.Fatalf("magpie provider after sync: %v\n%s", mp, readFile(path))
	}
	if mp["model"] != "magpie/deepseek/pro" {
		t.Fatalf("magpie provider's model after sync (the pick, keyed like model.name): %v", mp)
	}
	ms, _ := mp["models"].([]any)
	var later bool
	for _, m := range ms {
		if m.(map[string]any)["model"] == "later/m1" {
			later = true
		}
	}
	if !later {
		t.Fatalf("sync did not add later/m1: %v", ms)
	}
	if _, ok := f["modelProviders"]; ok {
		t.Fatalf("0.x modelProviders written over a 1.x wiring:\n%s", readFile(path))
	}
}

// The reporter's follow-ups on 1.0.0-preview.2 (macOS): the settings
// magpie writes are still all 0.x's schema, and a providers.magpie with
// the gateway key itself works — --list-models lists the model and -p
// gets through, with model.name keyed magpie/<wire id> (only prefixing
// model.name is not enough); and a bare wire id anywhere reads as
// provider <its first part> — "provider workbuddy is not available"
// until both model.name and the member's model were keyed
// magpie/<wire id> by hand. So the member magpie writes carries the key
// itself and the pick keyed magpie/<wire id>, beside the models[]
// catalog (preview.1's source takes model as the provider's default
// model beside it), and nothing goes to ~/.qwen/.env. Wiring the 0.x
// file a pre-1.x magpie left migrates it in one wire: magpie's entries,
// key and model.baseUrl go, providers.magpie comes.
func TestQwen1xPreview2(t *testing.T) {
	home, path := qwenHome(t)
	qwenPinVersion(t, "1.0.0-preview.2")
	os.WriteFile(path, []byte(fmt.Sprintf(`{
  "env": {
    "IDEALAB_API_KEY": "team-key",
    "MAGPIE_QWEN_API_KEY": "%s"
  },
  "modelProviders": {
    "openai": [
      {
        "id": "glm-5",
        "name": "[IdeaLab] glm-5",
        "baseUrl": "https://idealab.example.com/api/openai/v1",
        "envKey": "IDEALAB_API_KEY"
      },
      {
        "id": "deepseek/pro",
        "name": "pro · DeepSeek",
        "baseUrl": "%s",
        "envKey": "MAGPIE_QWEN_API_KEY",
        "generationConfig": { "contextWindowSize": 262144 }
      }
    ]
  },
  "model": {
    "name": "deepseek/pro",
    "baseUrl": "%s"
  },
  "security": { "auth": { "selectedType": "openai" } }
}
`, gateway.Token, gatewayV1(), gatewayV1())), 0o644)
	a := qwen(os.Getenv("HOME"))
	if err := a.Apply("model", "magpie/deepseek/pro"); err != nil {
		t.Fatal(err)
	}
	mp, rest, f := qwen1xFile(t, path)
	if mp == nil {
		t.Fatalf("no providers.magpie:\n%s", readFile(path))
	}
	if mp["apiKey"] != gateway.Token {
		t.Fatalf("apiKey is not the gateway key itself: %v", mp)
	}
	if mp["model"] != "magpie/deepseek/pro" {
		t.Fatalf("the member's model is not the pick keyed magpie/<wire id>: %v", mp)
	}
	if mp["baseUrl"] != gatewayV1() {
		t.Fatalf("magpie provider: %v", mp)
	}
	ms, _ := mp["models"].([]any)
	got := map[string]bool{}
	for _, m := range ms {
		got[m.(map[string]any)["model"].(string)] = true
	}
	for _, id := range []string{"deepseek/pro", "deepseek/flash", "anth/claude-sonnet-5"} {
		if !got[id] {
			t.Errorf("%s missing from models: %v", id, ms)
		}
	}
	// the user's own 0.x entry stays, magpie's goes
	mp0, _ := f["modelProviders"].(map[string]any)
	os0, _ := mp0["openai"].([]any)
	if len(os0) != 1 || os0[0].(map[string]any)["id"] != "glm-5" {
		t.Fatalf("0.x entries: %v", os0)
	}
	env := f["env"].(map[string]any)
	if env["MAGPIE_QWEN_API_KEY"] != nil || env["IDEALAB_API_KEY"] != "team-key" {
		t.Fatalf("env: %v", env)
	}
	m := f["model"].(map[string]any)
	if m["name"] != "magpie/deepseek/pro" {
		t.Fatalf("model: %v", m)
	}
	if _, ok := m["baseUrl"]; ok {
		t.Fatalf("0.x baseUrl left: %v", m)
	}
	// the key lives in the member: magpie writes nothing to ~/.qwen/.env
	if k, _ := edit.GetEnvFile(filepath.Join(home, ".qwen", ".env"), "MAGPIE_QWEN_API_KEY"); k != "" {
		t.Fatalf("key written to .env: %q", k)
	}
	if got := a.Values()["model"]; got != "magpie/deepseek/pro" {
		t.Fatalf("model read back: %q", got)
	}
	if d := a.Check(); d != "" {
		t.Fatalf("check: %s", d)
	}

	// reset: the member and magpie's 0.x wiring stay out, the user's own
	// entry and the model they had are back
	if err := a.Apply("model", ""); err != nil {
		t.Fatal(err)
	}
	mp, rest, f = qwen1xFile(t, path)
	if mp != nil {
		t.Fatalf("magpie provider left: %v", mp)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected members of providers after reset: %v", rest)
	}
	mp0, _ = f["modelProviders"].(map[string]any)
	os0, _ = mp0["openai"].([]any)
	if len(os0) != 1 || os0[0].(map[string]any)["id"] != "glm-5" {
		t.Fatalf("0.x entries after reset: %v", os0)
	}
	env = f["env"].(map[string]any)
	if env["MAGPIE_QWEN_API_KEY"] != nil || env["IDEALAB_API_KEY"] != "team-key" {
		t.Fatalf("env after reset: %v", env)
	}
	m = f["model"].(map[string]any)
	if m["name"] != "deepseek/pro" {
		t.Fatalf("model after reset: %v", m)
	}
	if k, _ := edit.GetEnvFile(filepath.Join(home, ".qwen", ".env"), "MAGPIE_QWEN_API_KEY"); k != "" {
		t.Fatalf("key left in .env: %q", k)
	}
}
