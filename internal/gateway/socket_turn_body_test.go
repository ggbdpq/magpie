package gateway

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// oldTurnBody is what a socket's turn wrote before turnBody.
func oldTurnBody(q map[string]json.RawMessage, input []json.RawMessage) ([]byte, error) {
	in, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	q["input"] = in
	return json.Marshal(q)
}

func longSocketTurn(items int) (map[string]json.RawMessage, []json.RawMessage) {
	frame := `{"type":"response.create","model":"fake/m1","stream":true,"instructions":"be <brief> & kind` + "\u2028\u2029" + `","input":[` +
		`{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}],` +
		`"tools":[ {"type": "function", "name": "a<b", "parameters": {"type":"object"}} ],"prompt_cache_key":null}`
	var q map[string]json.RawMessage
	if err := decodeJSONView([]byte(frame), &q); err != nil {
		panic(err)
	}
	line := strings.Repeat("读这个文件 if a < b && c > d { return \"x\\ty\" }\n", 20)
	var input []json.RawMessage
	for i := range items {
		// <, > and & as Codex sends them: as they are
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		e.Encode(line + strconv.Itoa(i) + "\u2028\u2029")
		text := bytes.TrimSpace(b.Bytes())
		// pretty-printed, as a reply's item may be
		input = append(input, json.RawMessage(`{ "type": "message",`+"\n\t"+`"role": "user", "content": [ {"type":"input_text", "text": `+string(text)+`} ] }`))
	}
	return q, input
}

// A socket's turn is written as json.Marshal wrote it, byte for byte:
// keys sorted, values compacted, <, >, & and U+2028, U+2029 escaped.
func TestTurnBodyIsJSONMarshal(t *testing.T) {
	for _, items := range []int{0, 1, 40} {
		q, input := longSocketTurn(items)
		want, err := oldTurnBody(q, input)
		if err != nil {
			t.Fatal(err)
		}
		q, input = longSocketTurn(items)
		got, err := turnBody(q, input)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%d items: %v\n got %.300s\nwant %.300s", items, err, got, want)
		}
	}
	q, _ := longSocketTurn(0)
	if got, err := turnBody(q, nil); err != nil || !bytes.Contains(got, []byte(`"input":null`)) {
		t.Errorf("nil input: %s %v", got, err)
	}
	q, input := longSocketTurn(2)
	input[1] = json.RawMessage(`{"bad"`)
	if _, err := turnBody(q, input); err == nil {
		t.Error("a broken item went upstream")
	}
}

// A long conversation on a socket is written once a turn: json.Marshal
// of the list and then of the body around it allocated ~5 copies.
func TestTurnBodyWrittenOnce(t *testing.T) {
	q, input := longSocketTurn(2000)
	body, _ := turnBody(q, input)
	size := uint64(len(body))
	got := allocatedEach(func() { turnBody(q, input) })
	if got > size*5/4 {
		t.Errorf("a %d-byte turn allocated %d bytes (%.1f×)", size, got, float64(got)/float64(size))
	}
	t.Logf("body %d: turnBody %d, json.Marshal %d", size, got, allocatedEach(func() { oldTurnBody(q, input) }))
}

// A socket turn's body is whole in memory already (turnBody's, in the
// request request built): readRequestBody handed it on as it is, rather
// than reading it out again — a second copy of the conversation whole,
// tens of MB for a long session, on every turn (#4668).
func TestSocketTurnBodyReadOnce(t *testing.T) {
	text := strings.Repeat("读这个文件 if a < b && c > d { return }\n", 40)
	var items []string
	for i := range 600 {
		items = append(items, `{"type":"message","role":"user","content":[{"type":"input_text","text":`+strconv.Quote(text+strconv.Itoa(i))+`}]}`)
	}
	body := []byte(`{"model":"fake/m1","stream":true,"instructions":"be brief","input":[` + strings.Join(items, ",") + `]}`)
	s := New()
	read := func() {
		req := httptest.NewRequest("POST", CodexPath+"/responses", nil)
		req.Body = newWholeBody(body)
		req.ContentLength = int64(len(body))
		got, ok := s.readRequestBody(httptest.NewRecorder(), req, provider.Responses, 0)
		if !ok || !bytes.Equal(got, body) {
			t.Fatalf("read %d bytes, ok %v; want the body as it is (%d bytes)", len(got), ok, len(body))
		}
	}
	read()
	if spent := allocatedEach(read); spent > uint64(len(body))/4 {
		t.Errorf("handing a %d-byte turn's body on allocated %d bytes", len(body), spent)
	}
}
