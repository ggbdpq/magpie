package provider

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/yetone/magpie/internal/plugin"
)

// #5103 (Jirentian): a plugin's provider shown as the plain generic glyph
// while every other provider wears its vendor's mark. A plugin that says a
// built-in icon's name (magpie bundles their SVGs) for its provider had it
// shown: the market already hands such a name on as it is, and the page
// draws it from its bundle; the auth hook's and package.json's were the
// only two places one reached. One that says none still falls back to the
// market's, as before.
func TestPluginSaidIconName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("MAGPIE_PLUGIN_MARKET", "off")
	was := fetchPluginIcon
	t.Cleanup(func() { fetchPluginIcon = was })
	// the fetch is refused, not answered with a success: a success would go
	// on in the background and keep the picture into this HOME after the
	// test is over, racing its TempDir's removal
	fetchPluginIcon = func(context.Context, string) (string, error) { return "", errors.New("no fetch in a test") }

	// a built-in icon's name passes as it is, where a URL is left to what
	// it was: the name is shown, the rest kept or fetched as pluginOwnIcon
	// does. A name no bundle has goes to the page as a market name does,
	// which draws its generic glyph for it.
	if ic := PluginIcon(plugin.Provider{ID: "deepseek", Spec: "lemon-plugin", Icon: "deepseek-color"}); ic != "deepseek-color" {
		t.Errorf("a built-in name = %q, want deepseek-color", ic)
	}
	if ic := PluginIcon(plugin.Provider{ID: "lemon", Spec: "lemon-plugin", Icon: "no-such-icon-name"}); ic != "no-such-icon-name" {
		t.Errorf("a bare name = %q, want it given as a market name is", ic)
	}
	if ic := PluginIcon(plugin.Provider{ID: "lemon", Spec: "lemon-plugin", Icon: "https://lemon.example/icon.png"}); ic != "" {
		t.Errorf("a picture said = %q, want it kept as a picture is (not yet fetched)", ic)
	}
}
