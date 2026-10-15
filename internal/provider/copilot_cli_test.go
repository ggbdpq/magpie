package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/testenv"
)

// The standalone CLI's sign-in: who in a JSONC config.json, the token in
// the keychain, sent as is under the CLI's integration.
func TestCopilotCLILogin(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	cli := filepath.Join(home, "copilot-home")
	t.Setenv("COPILOT_HOME", cli)
	os.MkdirAll(cli, 0o700)
	os.WriteFile(filepath.Join(cli, "config.json"), []byte(`// written by copilot
{
  "lastLoggedInUser": {"host": "https://github.com", "login": "octocat"},
  "loggedInUsers": [{"host": "https://github.com", "login": "octocat"}], // one
}`), 0o600)
	oldSecret := copilotCLISecret
	var asked string
	copilotCLISecret = func(account string) string { asked = account; return "gho_cli" }
	defer func() { copilotCLISecret = oldSecret }()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gho_cli" || r.Header.Get("Copilot-Integration-Id") != "copilot-developer-cli" {
			w.WriteHeader(401)
			return
		}
		w.Write([]byte(`{"data":[{"id":"claude-sonnet-5","name":"Claude Sonnet 5","model_picker_enabled":true,"capabilities":{"type":"chat"}}]}`))
	}))
	defer api.Close()
	user := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "token gho_cli" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"endpoints": map[string]string{"api": api.URL}})
	}))
	defer user.Close()
	oldUser, oldToken := CopilotUserURL, CopilotTokenURL
	CopilotUserURL, CopilotTokenURL = user.URL, "http://127.0.0.1:1/never"
	defer func() { CopilotUserURL, CopilotTokenURL = oldUser, oldToken }()
	copilotSessions = map[string]copilotSession{}

	p, ok := find(All(), "copilot")
	if !ok || p.Account.User != "octocat" || asked != "https://github.com:octocat" {
		t.Fatalf("copilot: %+v, asked %q", p, asked)
	}
	ms, err := p.Fetch(context.Background())
	if err != nil || len(ms) != 2 || ms[0].ID != "claude-sonnet-5" || ms[1].ID != CopilotAuto {
		t.Fatalf("models: %+v %v", ms, err)
	}
	req, _ := http.NewRequest("POST", p.Chat+"/chat/completions", nil)
	if err := p.Sign(context.Background(), req, Chat, []byte(`{"messages":[{"role":"user","content":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(req.URL.String(), api.URL) || req.Header.Get("Authorization") != "Bearer gho_cli" || req.Header.Get("Copilot-Integration-Id") != "copilot-developer-cli" {
		t.Fatalf("signed: %s %v", req.URL, req.Header)
	}

	// signed in with the token kept in plaintext: no keychain
	asked = ""
	os.WriteFile(filepath.Join(cli, "config.json"), []byte(`{"lastLoggedInUser":{"host":"https://github.com","login":"hubot"},"copilotTokens":{"https://github.com:hubot":"gho_plain"}}`), 0o600)
	if app, ok := copilotLogin(filepath.Join(home, ".config")); !ok || app.User != "hubot" || app.Token != "gho_plain" || asked != "" {
		t.Fatalf("plaintext: %+v asked %q", app, asked)
	}
}

// copilotSecretAsks is how many lines the keychain fake has been handed,
// one per run of it.
func copilotSecretAsks(t *testing.T, log string) int {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return 0
	}
	return strings.Count(strings.TrimSpace(string(b)), "\n") + 1
}

// A keychain read the user watched fail — the access prompt denied — is
// not asked again this run (#2408): asking again at the next account
// listing is what brought the dialog back every ten minutes. An item that
// isn't there (exit 44, nothing on screen) is still retried after the
// short wait, so a sign-in made later in the CLI is found.
func TestCopilotCLISecretDeniedNotAskedAgain(t *testing.T) {
	oldSecret := copilotCLISecret
	copilotCLISecret = copilotCLISecretReal
	defer func() { copilotCLISecret = oldSecret }()

	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	asked := filepath.Join(t.TempDir(), "asked.log")
	testenv.Program(t, filepath.Join(bin, "security"), `#!/bin/sh
echo "$@" >> '`+asked+`'
echo "security: SecKeychainItemCopyContent: User interaction is not allowed." >&2
exit 51
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	const acc = "https://github.com:octocat"
	if tok := copilotCLISecret(acc); tok != "" {
		t.Fatalf("a denied read answered %q", tok)
	}
	if n := copilotSecretAsks(t, asked); n != 1 {
		t.Fatalf("security ran %d times, want 1", n)
	}

	// the next account listing, after the ten-minute wait is over
	copilotSecrets.Lock()
	copilotSecrets.at[acc] = time.Now().Add(-11 * time.Minute)
	copilotSecrets.Unlock()
	if tok := copilotCLISecret(acc); tok != "" {
		t.Fatalf("read again after a denial answered %q", tok)
	}
	if n := copilotSecretAsks(t, asked); n != 1 {
		t.Fatalf("security ran %d times after a denial, want still 1", n)
	}
}

// The item not being there shows the user nothing, so its read is still
// retried once the short wait is over. Another login than the denial
// test's, so the two stand on their own whichever order they run in.
func TestCopilotCLISecretAbsentRetriedAfterTheWait(t *testing.T) {
	oldSecret := copilotCLISecret
	copilotCLISecret = copilotCLISecretReal
	defer func() { copilotCLISecret = oldSecret }()

	bin := filepath.Join(t.TempDir(), "bin")
	os.MkdirAll(bin, 0o755)
	asked := filepath.Join(t.TempDir(), "asked.log")
	testenv.Program(t, filepath.Join(bin, "security"), `#!/bin/sh
echo "$@" >> '`+asked+`'
echo "security: SecKeychainSearchCopyNext: The specified item could not be found in the keychain." >&2
exit 44
`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	const acc = "https://github.com:hubot"
	if tok := copilotCLISecret(acc); tok != "" {
		t.Fatalf("an absent item answered %q", tok)
	}
	copilotSecrets.Lock()
	copilotSecrets.at[acc] = time.Now().Add(-11 * time.Minute)
	copilotSecrets.Unlock()
	if tok := copilotCLISecret(acc); tok != "" {
		t.Fatalf("an absent item answered %q the second time", tok)
	}
	if n := copilotSecretAsks(t, asked); n != 2 {
		t.Fatalf("security ran %d times, want 2 (retried once the wait was over)", n)
	}
}

// On Linux the CLI 1.0's keyring store keys its token by service and
// username, not keytar's account (#723); a keyring that never answers
// doesn't hold the caller.
//
// The fakes are testenv.Programs: macOS checks a newly written program
// before its first exec, which under a loaded go test ./... took longer
// than copilotSecretWait, so both lookups ran out and the token read as ""
// (#1627). Which attribute is asked is no question of time, so the lookups
// that answer get a wait no loaded runner reaches; only the keyring that
// never answers is held to a short one.
func TestCopilotSecretToolUsername(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a shell script stands in for secret-tool")
	}
	old := copilotSecretWait
	defer func() { copilotSecretWait = old }()
	copilotSecretWait = 10 * time.Minute
	dir := t.TempDir()
	tool := filepath.Join(dir, "secret-tool")
	testenv.Program(t, tool, `#!/bin/sh
[ "$*" = "lookup service copilot-cli username https://github.com:octocat" ] && { echo gho_keyring; exit 0; }
exit 1
`)
	if got := copilotSecretTool(tool, "https://github.com:octocat"); got != "gho_keyring" {
		t.Fatalf("token %q, want the one under username", got)
	}
	if got := copilotSecretTool(tool, "https://github.com:hubot"); got != "" {
		t.Fatalf("another account's token: %q", got)
	}
	copilotSecretWait = 200 * time.Millisecond
	hang := filepath.Join(dir, "secret-tool-hang")
	testenv.Program(t, hang, "#!/bin/sh\nexec sleep 30\n")
	start := time.Now()
	// unbounded, the two lookups take 60s; bounded, 2.4s at most unloaded
	if got := copilotSecretTool(hang, "https://github.com:octocat"); got != "" || time.Since(start) > 30*time.Second {
		t.Fatalf("a keyring that never answers: %q after %v", got, time.Since(start))
	}
}

// magpie on Windows, the Copilot CLI signed in inside a WSL distro that
// runs (#723, jia2): WSL has no keyring, so the CLI kept its token in the
// distro's ~/.copilot/config.json, which magpie reads over WSL's share.
func TestCopilotCLIInWSL(t *testing.T) {
	home := signIn(t)
	os.Remove(filepath.Join(home, ".config", "github-copilot", "apps.json"))
	t.Setenv("COPILOT_HOME", filepath.Join(home, "no-copilot-here"))
	oldSecret := copilotCLISecret
	copilotCLISecret = func(string) string { return "" }
	distro := filepath.Join(t.TempDir(), "home", "jia")
	os.MkdirAll(filepath.Join(distro, ".copilot"), 0o700)
	os.WriteFile(filepath.Join(distro, ".copilot", "config.json"), []byte(`{
  "lastLoggedInUser": {"host": "https://github.com", "login": "jia2"},
  "loggedInUsers": [{"host": "https://github.com", "login": "jia2"}],
  "copilotTokens": {"https://github.com:jia2": "gho_wsl"}
}`), 0o600)
	oldHomes := CopilotWSLHomes
	homes := []string{filepath.Join(t.TempDir(), "other"), distro}
	CopilotWSLHomes = func() []string { return homes }
	copilotWSL.at = time.Time{}
	defer func() {
		copilotCLISecret, CopilotWSLHomes = oldSecret, oldHomes
		copilotWSL.at = time.Time{}
	}()

	app, ok := copilotLogin(filepath.Join(home, ".config"))
	if !ok || app.User != "jia2" || app.Token != "gho_wsl" || !app.cli {
		t.Fatalf("the WSL distro's Copilot CLI isn't found: %+v %v", app, ok)
	}
	if p, ok := find(All(), "copilot"); !ok || p.Account.User != "jia2" {
		t.Fatalf("copilot: %+v %v", p, ok)
	}

	// the distro stopped: it isn't listed, and isn't read once the
	// sign-in read before is older than copilotWSLAge
	homes = nil
	copilotWSL.at = time.Time{}
	if app, ok := copilotLogin(filepath.Join(home, ".config")); ok {
		t.Fatalf("a stopped distro's sign-in is used: %+v", app)
	}
}
