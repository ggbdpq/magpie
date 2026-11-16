package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/codexcat"
	"github.com/yetone/magpie/internal/provider"
)

func TestModelsPublishGroupEffortUpdates(t *testing.T) {
	fresh(t)
	relay, base := newRelay(t)
	manifest := strings.Replace(terraEntry, `"supports_reasoning_summaries":true`,
		`"supports_reasoning_summaries":true,"supports_reasoning_effort_updates":true`, 1)
	relay.manifest.Store(`{"models":[` + manifest + `]}`)
	if err := provider.Save(provider.Provider{
		ID: "relay", Name: "Relay", Key: "synthetic-key", Responses: base,
		Models: []string{"gpt-6.1-terra"},
	}); err != nil {
		t.Fatal(err)
	}
	fetchRelay(t)
	if err := provider.SaveGroup(provider.Group{
		Name: "supported", Members: []string{"relay/gpt-6.1-terra"},
	}); err != nil {
		t.Fatal(err)
	}
	handler := New().Handler()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("models: %d %s", recorder.Code, recorder.Body)
	}
	var list struct {
		Data []struct {
			ID      string `json:"id"`
			Updates bool   `json:"supports_reasoning_effort_updates"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range list.Data {
		if model.ID == "group/supported" {
			found = true
			if !model.Updates {
				t.Error("the remote model list lost the group's declared effort updates")
			}
		}
	}
	if !found {
		t.Fatal("the group is absent from the remote model list")
	}
	if entry := codexEntry(t, "group/supported"); entry["supports_reasoning_effort_updates"] != true {
		t.Error("the Codex model list lost the group's declared effort updates")
	}
	// 真实列表处理器经回环读取，确保 Remote 路径也保留声明。
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	models, err := catalog.FetchURL(context.Background(), server.URL+"/v1/models", "synthetic-key", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(codexcat.Catalog(models)), `"supports_reasoning_effort_updates": true`) {
		t.Error("the Remote catalog reader lost the published capability")
	}
}
