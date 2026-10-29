package gui

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/settings"
)

// Settings' Compact at refuses a size too small to hold an agent's system
// prompt (#4148: 400 typed for 400K) before anything is saved: Full
// window, on when the size was typed, stays on.
func TestCompactAtTooSmallLeavesFullWindow(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(h, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(h, ".cache"))
	if err := settings.Save(settings.Settings{FullContext: true, CompactAt: 500000}); err != nil {
		t.Fatal(err)
	}
	srv := Handler(nil, nil)
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/api/settings/full-context", strings.NewReader(`{"on":false,"at":400}`))
	r.RemoteAddr = "127.0.0.1:50000"
	srv.ServeHTTP(rec, r)
	if rec.Code == http.StatusOK || !strings.Contains(rec.Body.String(), "400k") {
		t.Errorf("400: %d %.200s", rec.Code, rec.Body)
	}
	if s := settings.Load(); !s.FullContext || s.CompactAt != 500000 {
		t.Errorf("a refused size changed the settings: full=%v at=%d", s.FullContext, s.CompactAt)
	}
}
