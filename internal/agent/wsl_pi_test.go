package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/edit"
	"github.com/yetone/magpie/internal/gateway"
	"github.com/yetone/magpie/internal/provider"
)

// piDistroHome is a distro's / in a temp dir with a user whose ~/.pi holds
// settings (none when ""); magpie's own HOME is another temp dir, whose
// ~/.pi nothing may touch.
func piDistroHome(t *testing.T, settings string) (root, home string) {
	t.Helper()
	syncHome(t)
	root = t.TempDir()
	home = filepath.Join(root, "home", "me")
	os.MkdirAll(filepath.Join(home, ".pi", "agent"), 0o755)
	if settings != "" {
		os.WriteFile(filepath.Join(home, ".pi", "agent", "settings.json"), []byte(settings), 0o644)
	}
	return root, home
}

// The probe asks after Pi as after Codex, and reads what it says.
func TestWSLProbeFindsPi(t *testing.T) {
	for _, want := range []string{`[ -d "$HOME/.pi" ] && echo dir:.pi`, `p=$(wslcmd pi) && echo "bin:pi $p"`,
		`[ -d "$HOME/.codex" ] && echo dir:.codex`, `p=$(wslcmd codex) && echo "bin:codex $p"`} {
		if !strings.Contains(wslProbeScript(), want) {
			t.Errorf("probe lacks %q:\n%s", want, wslProbeScript())
		}
	}
	d := parseProbe("U", "home:/home/me\ndir:.pi\nbin:pi\n")
	if d == nil || !d.Has["dir:.pi"] || !d.Has["bin:pi"] || !wslFound(*d) {
		t.Fatalf("%+v", d)
	}
}

// A running distro with Pi — its ~/.pi, or pi on its PATH alone — is
// listed as Pi · WSL <distro>, with the model Pi starts on; one with Codex
// and Pi has both; one with neither has none.
func TestWSLPiDiscovered(t *testing.T) {
	root, home := piDistroHome(t, `{"defaultProvider": "xai", "defaultModel": "grok-4.6", "defaultThinkingLevel": "high"}`)
	codexRoot := t.TempDir()
	os.MkdirAll(filepath.Join(codexRoot, "root", ".codex"), 0o755)
	os.MkdirAll(filepath.Join(codexRoot, "root", ".pi", "agent"), 0o755)
	os.WriteFile(filepath.Join(codexRoot, "root", ".codex", "config.toml"), []byte("model = \"gpt-5.5\"\n"), 0o644)
	binRoot := t.TempDir()
	asked, opened := fakeWSL(t, "Ubuntu\r\nArch\r\nBoth\r\nNone\r\n", "Ubuntu\r\nArch\r\nBoth\r\nNone\r\n", map[string]string{
		"Ubuntu": "home:/home/me\ndir:.pi\nroute:default via 172.20.0.1 dev eth0\n",
		"Arch":   "home:/home/a\nbin:pi\n",
		"Both":   "home:/root\ndir:.codex\nbin:codex\ndir:.pi\n",
		"None":   "home:/home/n\n",
	}, map[string]string{"Ubuntu": root, "Arch": binRoot, "Both": codexRoot})

	ds := wslDistros()
	if len(ds) != 3 || strings.Join(*opened, ",") != "Ubuntu,Arch,Both" || len(*asked) != 4 {
		t.Fatalf("distros %+v, asked %v, opened %v", ds, *asked, *opened)
	}
	var ids []string
	byID := map[string]*Agent{}
	for _, a := range wslAgentsOf(ds) {
		ids = append(ids, a.ID)
		byID[a.ID] = a
	}
	if got := strings.Join(ids, ","); got != "pi@wsl:Ubuntu,pi@wsl:Arch,codex@wsl:Both,pi@wsl:Both" {
		t.Fatalf("agents %s", got)
	}
	a := byID["pi@wsl:Ubuntu"]
	if a.Name != "Pi · WSL Ubuntu" || a.Icon != "pi" || a.WSL != "Ubuntu" || a.Bin != "" || a.UA != nil ||
		a.Path != filepath.Join(home, ".pi", "agent", "settings.json") {
		t.Fatalf("%+v", a)
	}
	if v := a.Values(); v["model"] != "xai/grok-4.6" || v["effort"] != "high" {
		t.Fatalf("values %v", v)
	}
	// Pi's model is kept apart from Codex's in the same distro
	byID["codex@wsl:Both"].Values()
	byID["pi@wsl:Both"].Values()
	wslSave()
	var kept map[string]*distro
	json.Unmarshal([]byte(readFile(wslStatePath())), &kept)
	if kept["Ubuntu"].Values["pi.model"] != "xai/grok-4.6" || kept["Both"].Values["model"] != "gpt-5.5" ||
		kept["Both"].Values["pi.model"] != "" || kept["None"] != nil {
		t.Fatalf("kept %s", readFile(wslStatePath()))
	}
}

// Picking one of magpie's models writes Pi's settings.json and models.json
// in the distro's home, the provider block the same as this machine's Pi
// gets (thinkingLevelMap and all) but naming the gateway as the distro
// reaches it: Windows' address under NAT, 127.0.0.1 when mirrored. This
// machine's ~/.pi is never touched.
func TestWSLPiPick(t *testing.T) {
	for _, mirrored := range []bool{false, true} {
		root, home := piDistroHome(t, `{"defaultProvider": "xai", "defaultModel": "grok-4.6"}`)
		if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1",
			Models: []string{"claude-sonnet-5"}}); err != nil {
			t.Fatal(err)
		}
		if err := provider.Save(provider.Provider{ID: "think", Name: "Think", Key: "k", Chat: "https://example.test/v1",
			Models: []string{"deep"}}); err != nil {
			t.Fatal(err)
		}
		if err := catalog.SaveLive("think", "https://example.test/v1", []catalog.Model{
			{ID: "deep", Efforts: []string{"none", "high", "max"}, Context: 1000000, Output: 64000}}); err != nil {
			t.Fatal(err)
		}
		d := distro{Name: "Ubuntu", Root: root, Home: "/home/me", Has: map[string]bool{"dir:.pi": true},
			Gateway: "172.20.0.1", Mirrored: mirrored, Running: true}
		a := wslPi(d)
		if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
			t.Fatal(err)
		}
		if err := a.Field("effort").Set("high"); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(home, ".pi", "agent")
		settings, models := filepath.Join(dir, "settings.json"), filepath.Join(dir, "models.json")
		if p, _ := edit.GetJSON(settings, "defaultProvider"); p != "magpie" {
			t.Fatalf("settings:\n%s", readFile(settings))
		}
		if m, _ := edit.GetJSON(settings, "defaultModel"); m != "relay/glm-4.6" {
			t.Fatalf("settings:\n%s", readFile(settings))
		}
		gw := gateway.URL()
		if !mirrored {
			gw = "http://172.20.0.1:" + gateway.Port()
		}
		var file struct {
			Providers map[string]map[string]any `json:"providers"`
		}
		if err := json.Unmarshal([]byte(readFile(models)), &file); err != nil {
			t.Fatal(err)
		}
		got := file.Providers["magpie"]
		if got["baseUrl"] != gw+"/v1" || got["apiKey"] != gateway.Token {
			t.Fatalf("mirrored %v: %v", mirrored, got)
		}
		// the same block as native Pi's, the gateway's address aside
		native, _ := json.Marshal(magpieProviderJSON("pi"))
		want := strings.ReplaceAll(string(native), `"`+gateway.URL(), `"`+gw)
		if b, _ := json.Marshal(got); string(b) != want {
			t.Fatalf("mirrored %v:\n got %s\nwant %s", mirrored, b, want)
		}
		if !strings.Contains(want, `"baseUrl":"`+gw+`"`) || !strings.Contains(want, `"thinkingLevelMap":{`) {
			t.Fatalf("no Anthropic model at the distro's gateway, or no thinking levels: %s", want)
		}
		if c := a.Check(); c != "" {
			t.Fatalf("mirrored %v: %s", mirrored, c)
		}
		// a sync keeps the distro's gateway
		if err := a.Sync(); err != nil || !strings.Contains(readFile(models), `"`+gw+`/v1"`) {
			t.Fatalf("sync %v:\n%s", err, readFile(models))
		}
		if !mirrored && !strings.Contains(a.Notice(), "mirrored") {
			t.Error(a.Notice())
		}
		own, _ := os.UserHomeDir()
		if _, err := os.Stat(filepath.Join(own, ".pi")); !os.IsNotExist(err) {
			t.Fatalf("this machine's ~/.pi was touched: %v", err)
		}
	}
}

// A stopped distro's Pi is never asked anything nor opened: it is listed
// as magpie last saw it, its options come from magpie alone, and a pick
// starts the distro, then writes its files.
func TestWSLPiStopped(t *testing.T) {
	root, home := piDistroHome(t, `{"defaultProvider": "xai", "defaultModel": "on-disk"}`)
	settings := filepath.Join(home, ".pi", "agent", "settings.json")
	before, _ := os.Stat(settings)
	b, _ := json.Marshal(map[string]*distro{
		"Stopped": {Name: "Stopped", Home: "/home/me", Root: root, Has: map[string]bool{"dir:.pi": true},
			Values: map[string]string{"pi.model": "xai/grok-4.6", "pi.effort": "low"}},
	})
	os.MkdirAll(filepath.Dir(wslStatePath()), 0o755)
	os.WriteFile(wslStatePath(), b, 0o600)
	asked, opened := fakeWSL(t, "Stopped\r\n", "", nil, nil)

	ds := wslDistros()
	if len(ds) != 1 || ds[0].Running || len(*asked) != 0 || len(*opened) != 0 {
		t.Fatalf("%+v asked %v opened %v", ds, *asked, *opened)
	}
	as := wslAgentsOf(ds)
	if len(as) != 1 {
		t.Fatalf("%d agents", len(as))
	}
	a := as[0]
	if a.ID != "pi@wsl:Stopped" || a.Icon != "pi" || a.Path != "" || a.Dir != "" || a.Check != nil || a.Sync != nil {
		t.Fatalf("%+v", a)
	}
	if v := a.Values(); v["model"] != "xai/grok-4.6" || v["effort"] != "low" {
		t.Fatalf("values %v", v)
	}
	if len(a.Field("model").Options(a.Values())) == 0 || !strings.Contains(a.Notice(), "isn't running") {
		t.Fatal("options / notice")
	}
	if a.Drift() != nil {
		t.Fatal("drift")
	}
	if len(*asked) != 0 || len(*opened) != 0 {
		t.Fatalf("asked %v opened %v", *asked, *opened)
	}
	if after, _ := os.Stat(settings); !after.ModTime().Equal(before.ModTime()) || readFile(settings) != `{"defaultProvider": "xai", "defaultModel": "on-disk"}` {
		t.Fatal("a stopped distro's files were touched")
	}

	if err := a.Field("model").Set("magpie/relay/glm-4.6"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(*asked, ",") != "Stopped" {
		t.Fatalf("not started first: %v", *asked)
	}
	if m, _ := edit.GetJSON(settings, "defaultModel"); m != "relay/glm-4.6" {
		t.Fatalf("set:\n%s", readFile(settings))
	}
	if !strings.Contains(readFile(filepath.Join(home, ".pi", "agent", "models.json")), `"baseUrl"`) {
		t.Fatal("no models.json")
	}
	if v := a.Field("model").Get(); v != "magpie/relay/glm-4.6" {
		t.Fatal(v)
	}
}

// linkedPiDistro is a running distro whose ~/.pi/agent/settings.json and
// models.json are links into a dotfiles repo (#5465), with the real files
// where the links lead. As through \\wsl.localhost, where a link the
// distro has can't be followed, the path as it is spelt can't be opened
// here: the links read as broken until magpie opens them where they lead.
// The probe's word of where they lead is handed in, as readlink -f there
// would have said it.
func linkedPiDistro(t *testing.T, where string, onDrive bool) (home, repo string) {
	t.Helper()
	syncHome(t)
	root, drive := t.TempDir(), t.TempDir()
	home = filepath.Join(root, "home", "me")
	agent := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(agent, 0o755); err != nil {
		t.Fatal(err)
	}
	if onDrive {
		repo = filepath.Join(drive, "Users", "me", "pi-config", "agent")
	} else {
		repo = filepath.Join(root, filepath.FromSlash(where))
	}
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "settings.json"), []byte(`{"packages":["pkg-a"],"defaultProvider":"xai","defaultModel":"grok-4.6"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "models.json"), []byte(`{"providers":{"custom":{"baseUrl":"https://example.com"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"settings.json", "models.json"} {
		if err := os.Symlink(where+"/"+f, filepath.Join(agent, f)); err != nil {
			t.Skip("no symlinks here:", err)
		}
	}
	probe := "home:/home/me\ndir:.pi\n" +
		"link:.pi/agent/settings.json " + where + "/settings.json\n" +
		"link:.pi/agent/models.json " + where + "/models.json\n"
	if onDrive {
		probe += "win:/mnt/c C:\n"
		t.Cleanup(FakeWSLDrives(map[string]string{"C:": drive}))
	}
	t.Cleanup(FakeWSL(map[string]string{"Ubuntu": probe}, map[string]string{"Ubuntu": root}))
	return home, repo
}

// Pi wired into a distro whose settings.json and models.json are links
// into a dotfiles repo (Temp0jd's #5465): the pick goes into what the
// links hold, keeping the links, the user's packages and other providers,
// and the row reads as wired — where the links lead on one of Windows'
// drives, and where they lead inside the distro.
func TestWSLPiLinkedConfig(t *testing.T) {
	for _, tc := range []struct {
		name, where string
		onDrive     bool
	}{
		{"on a Windows drive", "/mnt/c/Users/me/pi-config/agent", true},
		{"in the distro", "/data/pi-config/agent", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, repo := linkedPiDistro(t, tc.where, tc.onDrive)
			if err := provider.Save(provider.Provider{ID: "anth", Name: "Anth", Key: "k", Anthropic: "http://127.0.0.1:1",
				Models: []string{"claude-sonnet-5"}}); err != nil {
				t.Fatal(err)
			}
			a := wslAgentByID(t, "pi@wsl:Ubuntu")
			if a.Unreached() != nil {
				t.Fatalf("unreached: %v", a.Unreached())
			}
			if a.Path != filepath.Join(repo, "settings.json") {
				t.Fatalf("Pi's settings read at %q", a.Path)
			}
			if err := a.Pick("model", "magpie/relay/glm-4.6"); err != nil {
				t.Fatal(err)
			}
			for _, f := range []string{"settings.json", "models.json"} {
				fi, err := os.Lstat(filepath.Join(home, ".pi", "agent", f))
				if err != nil {
					t.Fatal(err)
				}
				if fi.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("%s was replaced by a regular file", f)
				}
			}
			if p, _ := edit.GetJSON(filepath.Join(repo, "settings.json"), "defaultProvider"); p != "magpie" {
				t.Fatalf("settings:\n%s", readFile(filepath.Join(repo, "settings.json")))
			}
			if m, _ := edit.GetJSON(filepath.Join(repo, "settings.json"), "defaultModel"); m != "relay/glm-4.6" {
				t.Fatalf("settings:\n%s", readFile(filepath.Join(repo, "settings.json")))
			}
			if !strings.Contains(readFile(filepath.Join(repo, "settings.json")), `"pkg-a"`) {
				t.Fatalf("the user's packages were wiped:\n%s", readFile(filepath.Join(repo, "settings.json")))
			}
			if b, _ := edit.GetJSON(filepath.Join(repo, "models.json"), "providers.custom.baseUrl"); b != "https://example.com" {
				t.Fatalf("the user's own provider was wiped:\n%s", readFile(filepath.Join(repo, "models.json")))
			}
			if _, has := edit.GetJSON(filepath.Join(repo, "models.json"), "providers.magpie"); !has {
				t.Fatalf("no magpie in models.json:\n%s", readFile(filepath.Join(repo, "models.json")))
			}
			if !a.Wired() {
				t.Fatal("the row reads as not wired")
			}
			if gone := unstash("pi@wsl:Ubuntu.before.absent"); gone != "" {
				t.Fatalf("the links were recorded as absent: %q", gone)
			}
		})
	}
}
