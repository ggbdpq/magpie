package gui

import (
	"fmt"
	"log"
	"slices"
	"strings"
	"time"
)

// magpie's windows are WebView2 on Windows, a runtime Windows 10 doesn't
// always have. Without it Wails hands the failure to ErrorHandler and exits,
// and the desktop build has no console for that to land in: a double-click
// came up to nothing (#1806, ufec). Run asks before any window is made and,
// missing, says so in a box of Windows' own.
//
// A WebView2 that is there but never finishes starting ends magpie the same
// way (#5773, BHXGGGGG: the controller wait timed out after 1m0s and magpie
// 0.1.3226, its gateway with it, was gone). webview2Exit says why in a box
// before that exit, and asks whether to start again.

// webView2Fatal reports whether err is one Wails kills magpie over on
// Windows: the WebView2 initialisation failures edge/chromium.go hands
// errorCallback before its os.Exit(1) — the controller wait timing out,
// the loader not answering, the runtime version not told, the browser path
// gone, the controller not made — name WebView2, the browser path or the
// controller; errors reported without exiting (a shortcut that didn't
// register, an event handler's panic) don't.
func webView2Fatal(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "webview2") ||
		strings.Contains(s, "browser path") ||
		strings.Contains(s, "error creating controller")
}

// webView2ExitNotice is the exit box's title and text in lang (trayLang's),
// English where it has none. It says magpie is about to end with its
// gateway, and asks whether to start it again.
func webView2ExitNotice(lang string) (title, body string) {
	t, ok := webView2ExitWords[lang]
	if !ok {
		t = webView2ExitWords["en"]
	}
	return t[0], t[1]
}

var webView2ExitWords = map[string][2]string{
	"en": {"magpie's WebView2 failed",
		"magpie's window could not start: the WebView2 runtime did not finish initialising in time.\r\n\r\n" +
			"magpie ends now. Its gateway and proxy go with it; starting it again brings them back.\r\n\r\n" +
			"Until then, \"magpie web\" opens magpie in your browser and \"magpie tui\" in a terminal.\r\n\r\n" +
			"Start magpie again now?"},
	"zh": {"magpie 的 WebView2 启动失败",
		"magpie 的窗口未能启动：WebView2 运行时没有在时限内完成初始化。\r\n\r\n" +
			"magpie 即将退出，它的网关与代理服务也随之结束；重新启动即可恢复。\r\n\r\n" +
			"在此之前，可以用 \"magpie web\" 在浏览器里打开 magpie，或用 \"magpie tui\" 在终端里打开。\r\n\r\n" +
			"现在重新启动 magpie 吗？"},
	"zh-TW": {"magpie 的 WebView2 啟動失敗",
		"magpie 的視窗未能啟動：WebView2 執行階段沒有在時限內完成初始化。\r\n\r\n" +
			"magpie 即將結束，它的閘道與代理服務也隨之結束；重新啟動即可恢復。\r\n\r\n" +
			"在此之前，可以用 \"magpie web\" 在瀏覽器裡開啟 magpie，或用 \"magpie tui\" 在終端機裡開啟。\r\n\r\n" +
			"現在重新啟動 magpie 嗎？"},
	"ja": {"magpie の WebView2 の起動に失敗しました",
		"magpie のウィンドウを起動できませんでした：WebView2 ランタイムが制限時間内に初期化を終えませんでした。\r\n\r\n" +
			"magpie は今すぐ終了します。ゲートウェイとプロキシも一緒に終わりますが、再度起動すれば戻ります。\r\n\r\n" +
			"それまでは \"magpie web\" でブラウザーから、\"magpie tui\" でターミナルから magpie を使えます。\r\n\r\n" +
			"今すぐ magpie を再起動しますか？"},
	"de": {"magpies WebView2 ist fehlgeschlagen",
		"Das Fenster von magpie ließ sich nicht starten: die WebView2 Runtime hat die Initialisierung nicht rechtzeitig abgeschlossen.\r\n\r\n" +
			"magpie endet jetzt. Sein Gateway und sein Proxy enden mit; ein erneuter Start bringt sie zurück.\r\n\r\n" +
			"Bis dahin öffnet \"magpie web\" magpie im Browser und \"magpie tui\" im Terminal.\r\n\r\n" +
			"magpie jetzt wieder starten?"},
}

// webview2ExitWait is how long the exit box gets answered: a box nobody
// clicks must not keep a half-started magpie hanging on it.
const webview2ExitWait = 5 * time.Minute

// runWebview2Exit asks ask whether to start magpie again and, on Yes,
// restarts; either way it runs onShutdown before returning to Wails' exit:
// that exit is os.Exit, which skips OnShutdown, and the failover Codex was
// left pointing at this process would retry a gateway that is gone. ask
// runs on a goroutine of its own (#1806: a box on Wails' window thread
// never finishes — ufec measured it hanging 122s there), and nobody
// answering within wait ends the same way.
func runWebview2Exit(ask func() bool, restart func() error, onShutdown func(), wait time.Duration) {
	answer := make(chan bool, 1)
	go func() { answer <- ask() }()
	retry := false
	select {
	case retry = <-answer:
	case <-time.After(wait):
		log.Println("magpie: nobody answered the WebView2 exit box in", wait)
	}
	if retry {
		if err := restart(); err != nil {
			log.Println("magpie: starting magpie again:", err)
		}
	}
	onShutdown()
}

// webView2Page is Microsoft's download page for the runtime.
const webView2Page = "https://developer.microsoft.com/microsoft-edge/webview2/"

// webView2Missing reads Wails' own answer, Env.Info().PlatformInfo's
// "WebView2": the runtime's version, "" when its lookup failed, the lookup
// a window's WebView2 environment makes. A key Wails doesn't set at all
// says nothing, and is read as there: refusing to start a magpie that would
// have worked is worse than the silent exit.
func webView2Missing(info map[string]any) bool {
	v, ok := info["WebView2"]
	return ok && fmt.Sprint(v) == ""
}

// webView2Notice is the box's title and text in lang (trayLang's), English
// where it has none. It names the page and winget's package (#1806: ufec
// asked for the winget way too), and asks whether to open the page; Yes
// opens it.
func webView2Notice(lang string) (title, body string) {
	t, ok := webView2Words[lang]
	if !ok {
		t = webView2Words["en"]
	}
	return t[0], fmt.Sprintf(t[1], webView2Page)
}

var webView2Words = map[string][2]string{
	"en": {"magpie needs WebView2",
		"magpie's windows need Microsoft Edge WebView2 Runtime, which isn't installed on this computer.\r\n\r\n" +
			"Install it from:\r\n%s\r\n\r\nor in a terminal:\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"Until then, \"magpie web\" opens magpie in your browser and \"magpie tui\" in a terminal.\r\n\r\n" +
			"Open the download page now?"},
	"zh": {"magpie 需要 WebView2",
		"magpie 的窗口需要 Microsoft Edge WebView2 运行时，这台电脑上没有安装。\r\n\r\n" +
			"请从这里安装：\r\n%s\r\n\r\n或在终端里运行：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"装好之前，可以用 \"magpie web\" 在浏览器里打开 magpie，或用 \"magpie tui\" 在终端里打开。\r\n\r\n" +
			"现在打开下载页面吗？"},
	"zh-TW": {"magpie 需要 WebView2",
		"magpie 的視窗需要 Microsoft Edge WebView2 執行階段，這台電腦上沒有安裝。\r\n\r\n" +
			"請從這裡安裝：\r\n%s\r\n\r\n或在終端機裡執行：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"裝好之前，可以用 \"magpie web\" 在瀏覽器裡開啟 magpie，或用 \"magpie tui\" 在終端機裡開啟。\r\n\r\n" +
			"現在開啟下載頁面嗎？"},
	"ja": {"magpie には WebView2 が必要です",
		"magpie のウィンドウには Microsoft Edge WebView2 ランタイムが必要ですが、このコンピューターにはインストールされていません。\r\n\r\n" +
			"こちらからインストールしてください：\r\n%s\r\n\r\nまたはターミナルで：\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"それまでは \"magpie web\" でブラウザーから、\"magpie tui\" でターミナルから magpie を使えます。\r\n\r\n" +
			"今すぐダウンロードページを開きますか？"},
	"de": {"magpie braucht WebView2",
		"Die Fenster von magpie brauchen die Microsoft Edge WebView2 Runtime, die auf diesem Computer nicht installiert ist.\r\n\r\n" +
			"Installieren Sie sie von:\r\n%s\r\n\r\noder im Terminal:\r\nwinget install Microsoft.EdgeWebView2Runtime\r\n\r\n" +
			"Bis dahin öffnet \"magpie web\" magpie im Browser und \"magpie tui\" im Terminal.\r\n\r\n" +
			"Die Download-Seite jetzt öffnen?"},
}

// webView2Args sorts what the user put in WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS
// (webview2env.Args) into Wails' three options, which it passes as the
// app's own arguments: the features to disable, the ones to enable, and
// every other argument as written. Wails' loader blanks the variable
// itself, so without this a user's --disable-features=RendererCodeIntegrity
// never reached magpie's renderer (#2753, LuYquan: an injected DLL got it
// killed and the window went black). Wails puts its own
// msSmartScreenProtection first in DisabledFeatures and writes the list as
// one --disable-features (and EnabledFeatures as one --enable-features):
// Chromium keeps only the last of a repeated switch, so two would drop one
// of them. A list given more than once is merged, each feature once, in
// the order given. Arguments are split as Windows splits a command line on
// spaces, a double-quoted run kept whole.
func webView2Args(env string) (disabled, enabled, other []string) {
	for _, a := range splitArgs(env) {
		switch k, v, _ := strings.Cut(a, "="); k {
		case "--disable-features":
			disabled = addFeatures(disabled, v)
		case "--enable-features":
			enabled = addFeatures(enabled, v)
		default:
			other = append(other, a)
		}
	}
	return disabled, enabled, other
}

// addFeatures adds a comma list's features to have, each once.
func addFeatures(have []string, list string) []string {
	for _, f := range strings.Split(strings.Trim(list, `"`), ",") {
		if f = strings.TrimSpace(f); f != "" && !slices.Contains(have, f) {
			have = append(have, f)
		}
	}
	return have
}

// splitArgs splits a command line on spaces and tabs outside double quotes,
// keeping each argument as written, its quotes too.
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case (r == ' ' || r == '\t' || r == '\n' || r == '\r') && !quoted:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
