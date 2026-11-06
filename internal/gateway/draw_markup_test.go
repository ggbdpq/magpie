package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// codexEasel is easeled's vendor behind a ChatGPT-account provider, whose
// images API is asked in JSON (drawImages' Codex branch).
var eUp string

func codexEasel(t *testing.T) (*Server, *easel) {
	t.Helper()
	fresh(t)
	e := &easel{}
	up := httptest.NewServer(e)
	t.Cleanup(up.Close)
	eUp = up.URL
	return New(), e
}

// #5364 (kongjianguan): /v1/images/edits takes a markup layer — a
// transparent PNG drawn over the source to steer the edit — but magpie
// read and forwarded only the mask: the JSON body's markup landed in
// drawing.Extra and neither the Codex JSON request nor the plain OpenAI
// multipart ever carried it, so a client steering an edit with a markup
// layer got a 200 that had quietly ignored it.
func TestCodexEditSendsTheMarkup(t *testing.T) {
	s, e := codexEasel(t)
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	p := provider.Provider{ID: "art", Name: "Art", Responses: eUp + "/v1", Key: "key",
		Account: &provider.Account{Agent: "codex", User: "u@example.com"}}
	mk, err := pictureOf(img)
	if err != nil {
		t.Fatal(err)
	}
	im, err := pictureOf(img)
	if err != nil {
		t.Fatal(err)
	}
	drawn, code, err := s.drawImages(context.Background(), p, "gpt-image-1", drawing{Prompt: "apply the annotation", Images: []picture{im}, Markup: &mk})
	if code != 200 || err != nil {
		t.Fatalf("%d %v", code, err)
	}
	_ = drawn
	sent := e.got("/v1/images/edits")
	if len(sent) != 1 {
		t.Fatalf("vendor was sent %v", sent)
	}
	var req map[string]any
	if err := json.Unmarshal([]byte(sent[0]), &req); err != nil {
		t.Fatalf("vendor got non-JSON: %v (%s)", err, sent[0])
	}
	if _, ok := req["markup"]; !ok {
		t.Errorf("the Codex request carries no markup: %s", sent[0])
	}
}

// the plain OpenAI multipart edit carries it as a file part, beside the
// mask.
func TestEditSendsTheMarkup(t *testing.T) {
	s, e := easeled(t)
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw := postImages(t, s, "/v1/images/edits", "application/json",
		`{"model":"art/gpt-image-1","prompt":"apply the annotation","images":[{"image_url":"`+img+`"}],"markup":{"image_url":"`+img+`"}}`)
	if code != 200 {
		t.Fatalf("%d %s", code, raw)
	}
	sent := e.got("/v1/images/edits")
	if len(sent) != 1 || !strings.Contains(sent[0], `name="markup"`) {
		t.Fatalf("the markup layer never reached the vendor: %v", sent)
	}
}

// an unreadable markup is named, not dropped.
func TestEditWithBrokenMarkupIsRefused(t *testing.T) {
	s, _ := codexEasel(t)
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	code, _, raw := postImages(t, s, "/v1/images/edits", "application/json",
		`{"model":"art/gpt-image-1","prompt":"x","images":[{"image_url":"`+img+`"}],"markup":{"image_url":"data:image/png;base64,not-base64"}}`)
	if code != 400 {
		t.Fatalf("broken markup: %d, want 400 (%s)", code, raw)
	}
}
