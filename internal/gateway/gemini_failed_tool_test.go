package gateway

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// A rejected tool generation is a failed response, even when Gemini emitted
// a progress sentence first. It must not look like a completed agent turn.
func TestGeminiFailedToolDecoder(t *testing.T) {
	for _, reason := range []string{"MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL"} {
		for _, wrapped := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/wrapped=%v", reason, wrapped), func(t *testing.T) {
				d := &codeAssistDecoder{}
				var events []Event
				emit := func(e Event) { events = append(events, e) }
				chunk := `{"candidates":[{"content":{"parts":[{"text":"I am writing the file."},{"functionCall":{"name":"Write","args":{}}}]},"finishReason":"` + reason + `"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":6100,"cachedContentTokenCount":80}}`
				if wrapped {
					chunk = `{"response":` + chunk + `}`
				}
				if err := d.decode(chunk, emit); err != nil {
					t.Fatal(err)
				}
				if err := d.decode(`{"candidates":[{"finishReason":"`+reason+`"}]}`, emit); err != nil {
					t.Fatal(err)
				}
				errors, usage := 0, 0
				for _, e := range events {
					switch e.Kind {
					case KError:
						errors++
						if !strings.Contains(e.Text, reason) {
							t.Fatalf("error lost reason: %+v", e)
						}
					case KToolStart:
						t.Fatal("rejected call was forwarded for execution")
					case KStop:
						t.Fatalf("failed generation was presented as a normal stop: %+v", e)
					case KUsage:
						usage++
						if e.Usage.Output != 6100 || e.Usage.CacheRead != 80 {
							t.Fatalf("usage lost: %+v", e.Usage)
						}
					}
				}
				if errors != 1 || usage != 1 {
					t.Fatalf("errors=%d usage=%d, want one each", errors, usage)
				}
			})
		}
	}
}

func TestGeminiFailedToolToAnthropic(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, preface := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/preface=%v", stream, preface), func(t *testing.T) {
				noRests()
				t.Cleanup(noRests)
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				t.Setenv("XDG_CACHE_HOME", t.TempDir())
				var asked atomic.Int32
				// The rejected call's braces are left open on purpose: a call
				// whole in finishMessage is salvaged and made the call (the
				// gateway's own answer, kept), so this pins the unparsable
				// rest — the name still shows, the rejected body never does.
				f := &fake{t: t, reply: sse(`data: {"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: call:default_api:Write{content: PRIVATE_BODY"}],"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":6100}}`)}
				if preface {
					f.reply = sse(`data: {"candidates":[{"content":{"parts":[{"text":"I am writing the file."}]}}]}`) + f.reply
				}
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Add(1); f.ServeHTTP(w, r) }))
				t.Cleanup(up.Close)
				p := provider.Provider{ID: "gem", Name: "Gem", Key: "synthetic", Gemini: up.URL + "/v1beta", Models: []string{"gemini-test"}}
				if err := provider.Save(p); err != nil {
					t.Fatal(err)
				}
				srv := New()
				rec := httptest.NewRecorder()
				srv.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":"gem/gemini-test","stream":%v,"max_tokens":32,"messages":[{"role":"user","content":"write a file"}]}`, stream))))
				status, body := rec.Code, rec.Body.String()
				t.Logf("client status=%d", status)
				calls := srv.Recent()
				if asked.Load() != 1 {
					t.Fatalf("failed generation was retried %d times", asked.Load())
				}
				if len(calls) != 1 || calls[0].Usage.Output != 6100 || calls[0].Usage.Input != 100 || calls[0].Usage.Stop != "MALFORMED_FUNCTION_CALL" {
					t.Fatalf("failed request usage or stop lost: %+v", calls)
				}
				if stream {
					if status != http.StatusOK && status != http.StatusBadRequest {
						t.Fatalf("unexpected stream status %d", status)
					}
				} else if status != http.StatusBadRequest {
					t.Fatalf("generation failure should be non-retryable: %d %s", status, body)
				}
				if !strings.Contains(body, "invalid_request_error") || !strings.Contains(body, "MALFORMED_FUNCTION_CALL") || !strings.Contains(body, "tool Write") || strings.Contains(body, `"stop_reason":"end_turn"`) {
					t.Fatalf("incorrect failure: %s", body)
				}
				if strings.Contains(body, "PRIVATE_BODY") || strings.Contains(body, "call:default_api:") {
					t.Fatalf("raw rejected call exposed: %s", body)
				}
				c := candidate{p: p, model: "gemini-test"}
				if r, ok := restOf(c.restKey()); ok {
					t.Fatalf("healthy provider rested: %+v", r)
				}
				if retryable(http.StatusBadRequest, []byte(body)) {
					t.Fatal("failed generation was retryable")
				}
				if r := srv.restAfter(c, http.StatusBadRequest, nil, []byte(body)); r.By != "off" {
					t.Fatalf("generation failure rests provider: %+v", r)
				}
			})
		}
	}
}

func TestGeminiUpstreamStop(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		body := `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`
		if wrapped {
			body = `{"response":` + body + `}`
		}
		if got := upstreamStop([]byte(body)); got != "MALFORMED_FUNCTION_CALL" {
			t.Fatalf("stop=%q", got)
		}
	}
	if got := upstreamStop([]byte(`{"candidates":[{"content":{"parts":[{"text":"finishReason: MALFORMED_FUNCTION_CALL"}]}}]}`)); got != "" {
		t.Fatalf("text treated as stop: %q", got)
	}
}

func TestGeminiFailureIsTerminalForContent(t *testing.T) {
	r := deferredRequest(t, tsRequest([]string{tsTools0, tsReadA, tsPlaceholder}, tsTurn1, tsSearchA))
	for _, first := range []string{
		`{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"call:default_api:magpie_deferred_tool{\"name\":\"unknown\",\"arguments\":{}}","thoughtSignature":"must-not-leak"}]},"groundingMetadata":{"webSearchQueries":["ignored"],"groundingChunks":[{"web":{"title":"ignored","uri":"https://example.invalid"}}]}}]}`,
		`{"error":{"code":400,"message":"invalid generation"}}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"magpie_deferred_tool","args":{"name":"unknown","arguments":{}}}}]}}]}`,
	} {
		d := &codeAssistDecoder{deferred: deferredForGemini(r)}
		var events []Event
		for _, chunk := range []string{first, first, `{"error":{"code":400,"message":"late error"}}`, `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"Write","args":{"content":"must not execute"}}},{"text":"must not be shown"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":6000}}`} {
			d.decode(chunk, func(e Event) { events = append(events, e) })
		}
		errors, usage := 0, 0
		for _, e := range events {
			switch e.Kind {
			case KError:
				errors++
			case KUsage:
				usage++
			case KToolStart, KText, KStop, KTextSig, KSearch:
				t.Fatalf("content escaped after failure: %+v", e)
			}
		}
		if errors != 1 || usage != 1 {
			t.Fatalf("errors=%d usage=%d", errors, usage)
		}
	}
}
