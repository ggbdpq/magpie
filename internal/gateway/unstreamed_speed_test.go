package gateway

import (
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// A reply that wasn't streamed has no first token, so it told no speed at
// all: DecodeOf answered nothing for it, noteSpeed kept nothing, and a key
// whose caller streams nothing stayed unmeasured for good — Speed first
// kept sending everything to the first key in the list, "the last ones
// never having answered" (#5575). Its pace is its output over its whole
// wait, the reading the reporter gave the Requests page, and it orders a
// seat by though no first token was told.
func TestAnUnstreamedReplyTellsItsPace(t *testing.T) {
	forgetSpeeds()
	t.Cleanup(forgetSpeeds)
	c := candidate{p: provider.Provider{ID: "v"}, rest: "v", model: "m"}
	t0 := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)

	// noteSpeed keeps the pace of a reply with no first token
	noteSpeed(c, 0, 50, 1000, t0)
	ttft, tps := speedOf(c, t0)
	if ttft != 0 {
		t.Fatalf("an unstreamed reply told a first token: %v", ttft)
	}
	if math.Abs(tps-50) > 0.01 {
		t.Fatalf("an unstreamed reply told no pace: %v, want 50 tokens a second", tps)
	}

	// its pace decays against its own samples like a streamed one's
	noteSpeed(c, 0, 125, 1000, t0.Add(speedHalfLife))
	if _, tps := speedOf(c, t0.Add(speedHalfLife)); math.Abs(tps-100) > 0.01 {
		t.Fatalf("after a half-life: tps %v, want (50*0.5+125)/1.5 = 100", tps)
	}

	// noteTrySpeed measures a whole unstreamed try (no ttft, no first
	// text) at the try's own pace
	forgetSpeeds()
	noteTrySpeed(c, Try{Millis: 4000}, 100, 0, t0)
	ttft, tps = speedOf(c, t0)
	if ttft != 0 || tps != 25 {
		t.Fatalf("an unstreamed try: ttft %v tps %v, want 0 and 25", ttft, tps)
	}

	// nothing said, nothing kept: a failed or empty reply tells no pace
	forgetSpeeds()
	noteTrySpeed(c, Try{Millis: 4000}, 0, 0, t0)
	if _, tps := speedOf(c, t0); tps != 0 {
		t.Fatalf("a reply of no output told a pace: %v", tps)
	}

	// stale is stale, whichever sample was the last
	forgetSpeeds()
	noteSpeed(c, 0, 50, 1000, t0)
	if _, tps := speedOf(c, t0.Add(speedStale+time.Second)); tps != 0 {
		t.Fatalf("an hour and more on, still measured: %v", tps)
	}
}

// plainReply is a vendor that answers one chat completion whole, no
// stream: no first token is ever told it. A few ms, for a whole
// millisecond's wait to be measured, as streamed's is.
func plainReply(w http.ResponseWriter, r *http.Request) {
	io.Copy(io.Discard, r.Body)
	time.Sleep(3 * time.Millisecond)
	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, `{"id":"c","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":20,"total_tokens":23}}`)
}

// A Speed first group whose callers stream nothing: the first answer
// measures the key that gave it — no first token, its whole wait for a
// pace — and the next request goes to the faster key, though it is listed
// second. Before, no key of such a pool was ever measured, and Speed
// first went on sending the first in the list everything (#5575).
func TestSpeedFirstMeasuresAnUnstreamedAnswer(t *testing.T) {
	fresh(t)
	forgetSpeeds()
	t.Cleanup(forgetSpeeds)
	serveOn(t, "slowv", "sk-slow", []string{"m"}, http.HandlerFunc(plainReply))
	serveOn(t, "fastv", "sk-fast", []string{"m"}, http.HandlerFunc(plainReply))
	if err := provider.SaveGroup(provider.Group{
		Name: "G", Members: []string{"slowv/m", "fastv/m"}, Routing: provider.Fastest,
	}); err != nil {
		t.Fatal(err)
	}
	s := New()
	ask := func() *Route {
		t.Helper()
		code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"x"}]}`)
		if code != 200 {
			t.Fatalf("code %d: %s", code, body)
		}
		return s.trace.routes[len(s.trace.routes)-1]
	}
	// none measured: the list, and that answer is measured
	if first := ask(); first.Order[0].Provider != "slowv" {
		t.Fatalf("with nothing measured Speed first left the list: %+v", first.Order)
	}
	speeds.Lock()
	said := 0
	for id, r := range speeds.m {
		if strings.HasPrefix(id, "slowv") && r.tps > 0 && r.ttft == 0 {
			said++
		}
	}
	speeds.Unlock()
	if said == 0 {
		t.Fatal("the unstreamed answer measured no pace")
	}
	// slowv's pace is made slow and fastv's fast — fastv's from an answer
	// it gave earlier, measured the same way — and the next request goes
	// to fastv, though the list puts slowv first
	var slowID string
	speeds.Lock()
	for id := range speeds.m {
		if strings.HasPrefix(id, "slowv") {
			slowID = id
		}
	}
	speeds.Unlock()
	if slowID == "" {
		t.Fatal("the unstreamed answer measured no pace")
	}
	now := time.Now()
	speeds.Lock()
	speeds.m[slowID] = speedRec{tps: 10, tpsW: 5, tpsAt: now}
	speeds.m[strings.Replace(slowID, "slowv", "fastv", 1)] = speedRec{tps: 100, tpsW: 5, tpsAt: now}
	speeds.Unlock()
	if last := ask(); last.Order[0].Provider != "fastv" {
		t.Fatalf("Speed first went to %q first, want fastv by its told pace: %+v", last.Order[0].Provider, last.Order)
	}
}
