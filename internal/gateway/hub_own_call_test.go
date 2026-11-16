package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// hieuthahn (#6885): magpie's own web search takes 10–65 s a call, and the
// hub held every call to hubCallWait (50s), so about 1 call in 10 died with
// "it didn't answer in 50s" though the direct path answers the same call
// and the caller's client waits 60s. A call to one of magpie's own servers
// runs under the caller's own timeout alone; the hub's cap stays for the
// Library's other servers, so their agent is still told which server
// didn't answer before its client gives up.
func TestHubMCPOwnServerCallRunsToTheCaller(t *testing.T) {
	fresh(t)
	f := &fakeHub{on: true, tools: map[string][]HubTool{"claude": {
		{Server: "magpie-web-search", Tool: "web_search", Spec: hubSpec("web_search"), Own: true},
		{Server: "relay", Tool: "wait", Spec: hubSpec("wait")},
	}}}
	f.call = func(ctx context.Context, server, tool string) (json.RawMessage, error) {
		_, has := ctx.Deadline()
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("cut, deadline had=%v", has)
		case <-time.After(80 * time.Millisecond):
			text := "{\"content\":[{\"type\":\"text\",\"text\":\"deadline had=" + fmt.Sprint(has) + "\"}]}"
			return json.RawMessage(text), nil
		}
	}
	withHub(t, f)
	wait := hubCallWait
	hubCallWait = 30 * time.Millisecond
	t.Cleanup(func() { hubCallWait = wait })
	h := lanGuard(New().Handler())
	m := readMCP(t, hubPost(h, "claude", here, toolsCall("magpie-web-search__web_search", `{}`)))
	if m.Error != nil || m.Result.IsError || !strings.Contains(m.Result.Content[0].Text, "deadline had=false") {
		t.Errorf("magpie's own server was held to the hub's cap: %+v", m)
	}
	m = readMCP(t, hubPost(h, "claude", here, toolsCall("relay__wait", `{}`)))
	if !m.Result.IsError || m.Result.Content[0].Text != "The MCP server relay couldn't run wait: it didn't answer in 30ms" {
		t.Errorf("a Library server held to the cap: %+v", m)
	}
}
