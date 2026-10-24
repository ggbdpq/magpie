package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// #3447: a web search's text block carries no citations[], so a client
// that reads citations[].cited_text for its snippets (dsh's
// web-search-deepseek) is left with urls alone. Where magpie searches for
// the model, the blocks are magpie's own making: an excerpt a searcher
// quoted of a page, or a search API's text of it, is carried as the
// citations of the text that read it. A search that quoted nothing gives
// no citation, as none is invented.

// citationSearcher serves as a searcher's Anthropic API, answering the
// search with the pages it found, each with the excerpt quoted of it when
// quoted, else sealed as DeepSeek's are, with nothing quoted.
func citationSearcher(t *testing.T, quoted bool) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			io.WriteString(w, `{"data":[{"id":"m1"}]}`)
			return
		}
		b, _ := io.ReadAll(r.Body)
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") || !strings.Contains(string(b), `"web_search_20250305"`) {
			http.NotFound(w, r)
			return
		}
		quote := func(cited, sealed string) string {
			if quoted {
				return `,"cited_text":"` + cited + `"`
			}
			return `,"encrypted_content":"` + sealed + `"`
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"m","type":"message","role":"assistant","model":"m1","content":[
			{"type":"server_tool_use","id":"call_1","name":"web_search","input":{"query":"今天上海天气"}},
			{"type":"web_search_tool_result","tool_use_id":"call_1","content":[
				{"type":"web_search_result","title":"上海天气预报","url":"https://www.weather.com.cn/weather/101020100.shtml","page_age":null`+quote("上海今天小雨，22/17℃。", "SEALED")+`},
				{"type":"web_search_result","title":"上海当前天气","url":"http://pc.weathercn.com/weather/106577/","page_age":"October 3, 2026"`+quote("多云转晴，风力三到四级。", "SEALED-2")+`}]},
			{"type":"text","text":"上海今天小雨。"}],
			"stop_reason":"end_turn","usage":{"input_tokens":100,"output_tokens":50}}`)
	}))
	t.Cleanup(srv.Close)
	glm, _ := newBlindModel(t)
	searchesOn(t, provider.Anthropic, srv.URL)
	save(t,
		provider.Provider{ID: "searcher", Name: "Searcher", Key: "k", Anthropic: srv.URL + "/anthropic", Models: []string{"m1"}},
		provider.Provider{ID: "glm", Name: "GLM", Key: "k", Chat: glm.URL, Models: []string{"glm-flash"}})
}

const citationQuery = `{"model":"glm/glm-flash","max_tokens":3000,"stream":%t,"messages":[{"role":"user","content":"用 web_search 工具搜索：今天上海天气"}],"tools":[{"type":"web_search_20250305","name":"web_search"}]}`

// searchEvents reads a streamed reply: the block each start began, the
// deltas in order.
func searchEvents(t *testing.T, body string) (starts map[string]map[string]any, deltas []map[string]any, results []map[string]any) {
	t.Helper()
	starts = map[string]map[string]any{}
	for _, line := range strings.Split(body, "\n") {
		data, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		var ev struct {
			Type         string         `json:"type"`
			ContentBlock map[string]any `json:"content_block"`
			Delta        map[string]any `json:"delta"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "content_block_start":
			starts[ev.ContentBlock["type"].(string)] = ev.ContentBlock
			if ev.ContentBlock["type"] == "web_search_tool_result" {
				results = append(results, ev.ContentBlock)
			}
		case "content_block_delta":
			deltas = append(deltas, ev.Delta)
		}
	}
	return starts, deltas, results
}

func citationOf(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var c map[string]any
	if json.Unmarshal(b, &c) != nil {
		t.Fatalf("citation %v", v)
	}
	return c
}

// The searcher's excerpts reach the client as the citations of the text
// that read it, streamed: each a web_search_result_location naming the
// page and what was quoted of it, ahead of the text itself.
func TestWebSearchCitationsCarriedToText(t *testing.T) {
	searchSandbox(t)
	citationSearcher(t, true)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(fmt.Sprintf(citationQuery, true))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	starts, deltas, results := searchEvents(t, rec.Body.String())
	text := starts["text"]
	if text == nil {
		t.Fatal("no text block")
	}
	if _, ok := text["citations"]; !ok {
		t.Fatalf("the text block carries no citations: %v", text)
	}
	var textSeen bool
	var cites []map[string]any
	for _, d := range deltas {
		switch d["type"] {
		case "citations_delta":
			if textSeen {
				t.Fatalf("a citation after its text: %v", d)
			}
			cites = append(cites, citationOf(t, d["citation"]))
		case "text_delta":
			textSeen = true
		}
	}
	if len(cites) != 2 {
		t.Fatalf("citations %v", cites)
	}
	for i, want := range []map[string]string{
		{"url": "https://www.weather.com.cn/weather/101020100.shtml", "title": "上海天气预报", "cited_text": "上海今天小雨，22/17℃。"},
		{"url": "http://pc.weathercn.com/weather/106577/", "title": "上海当前天气", "cited_text": "多云转晴，风力三到四级。"},
	} {
		c := cites[i]
		if c["type"] != "web_search_result_location" || c["url"] != want["url"] ||
			c["title"] != want["title"] || c["cited_text"] != want["cited_text"] {
			t.Fatalf("citation %d: %v", i, c)
		}
	}
	if len(results) == 0 {
		t.Fatal("no web_search_tool_result block")
	}
	content, _ := results[0]["content"].([]any)
	first, _ := content[0].(map[string]any)
	if first["cited_text"] != "上海今天小雨，22/17℃。" {
		t.Fatalf("the result carries no excerpt: %v", first)
	}
	if first["encrypted_content"] != "" {
		t.Fatalf("the searcher's sealed content reached the client: %v", first)
	}
}

// The same on a reply asked whole.
func TestWebSearchCitationsWholeReply(t *testing.T) {
	searchSandbox(t)
	citationSearcher(t, true)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(fmt.Sprintf(citationQuery, false))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	var cites []any
	for _, b := range out.Content {
		if b["type"] == "text" {
			c, _ := b["citations"].([]any)
			cites = c
		}
	}
	if len(cites) != 2 {
		t.Fatalf("citations %v", cites)
	}
	c := citationOf(t, cites[0])
	if c["type"] != "web_search_result_location" || c["url"] != "https://www.weather.com.cn/weather/101020100.shtml" ||
		c["cited_text"] != "上海今天小雨，22/17℃。" {
		t.Fatalf("citation %v", c)
	}
}

// A search that quoted nothing of its pages gives no citation: none is
// invented, and the sealed content of the searcher's pages stays sealed.
func TestWebSearchWithoutExcerptsGivesNoCitations(t *testing.T) {
	searchSandbox(t)
	citationSearcher(t, false)
	rec := httptest.NewRecorder()
	New().Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages",
		strings.NewReader(fmt.Sprintf(citationQuery, true))))
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	starts, deltas, results := searchEvents(t, rec.Body.String())
	if starts["text"] == nil {
		t.Fatal("no text block")
	}
	if _, ok := starts["text"]["citations"]; ok {
		t.Fatalf("citations invented: %v", starts["text"])
	}
	for _, d := range deltas {
		if d["type"] == "citations_delta" {
			t.Fatalf("a citation invented: %v", d)
		}
	}
	if len(results) == 0 {
		t.Fatal("no web_search_tool_result block")
	}
	content, _ := results[0]["content"].([]any)
	first, _ := content[0].(map[string]any)
	if _, ok := first["cited_text"]; ok {
		t.Fatalf("an excerpt invented: %v", first)
	}
	if first["encrypted_content"] != "" {
		t.Fatalf("the searcher's sealed content reached the client: %v", first)
	}
}
