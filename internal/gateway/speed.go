package gateway

import (
	"math"
	"sync"
	"time"

	"github.com/yetone/magpie/internal/usage"
)

// Speed lately, for Speed first (routes.Fastest): of each account or key
// and model (restID), a decayed average of its answers' time to first
// token and of how fast they wrote. Kept in memory, a few numbers each,
// written once as an answer ends (noteSpeed) and read with a map lookup
// per candidate as a pool routed by speed is weighed: the usage history
// is never read for it. A restart forgets them, and each is measured
// again as Fastest's probes give it a turn.

// speedHalfLife: a sample counts half after this, against those since.
// speedStale: this long after its last answer a seat counts as not
// measured lately, and Fastest gives it a turn now and then to be
// measured again, so one that was slow once isn't kept last for good.
const (
	speedHalfLife = 20 * time.Minute
	speedStale    = time.Hour
)

// speedMost bounds the records: past it, those not measured lately go.
const speedMost = 4096

type speedRec struct {
	ttft, ttftW   float64   // the average ms to the first token, and its samples' weight
	tps, tpsW     float64   // the average output tokens a second, and its samples' weight
	whole, wholeW float64   // the same, of answers that weren't streamed: output over the whole wait
	at, tpsAt     time.Time // the last sample of each
	wholeAt       time.Time
}

var speeds = struct {
	sync.Mutex
	m map[string]speedRec // candidate restID → its speed lately
}{m: map[string]speedRec{}}

func decayed(w float64, since time.Duration) float64 {
	if since <= 0 {
		return w
	}
	return w * math.Exp2(-since.Seconds()/speedHalfLife.Seconds())
}

// fold is avg of weight w, decayed by since, with x added at weight 1.
func fold(avg, w float64, since time.Duration, x float64) (float64, float64) {
	w = decayed(w, since)
	return (avg*w + x) / (w + 1), w + 1
}

// fresh is when the record was measured last, whichever of its samples
// that was: a pace can come without a first token (a reply that wasn't
// streamed, #5575), and the record it makes is none the less recent.
func (r speedRec) fresh() time.Time {
	f := r.at
	if r.tpsAt.After(f) {
		f = r.tpsAt
	}
	if r.wholeAt.After(f) {
		f = r.wholeAt
	}
	return f
}

// noteSpeed records an answer of c's: ttft ms from its try's start to its
// first content, and, when its pace was told, tokens written in w ms
// after it (usage.DecodeOf). A reply that wasn't streamed has no ttft,
// and tells no ttft, but its pace is kept: it is what Speed first has
// from a caller that streams nothing (#5575). The two paces are kept
// apart: an unstreamed answer's pace is its output over its whole wait,
// the first-token wait included, and folded into a streamed answer's
// average it would drag the finer reading down (the review of #5575).
func noteSpeed(c candidate, ttft int64, tokens int, w int64, now time.Time) {
	if ttft <= 0 && (tokens <= 0 || w <= 0) {
		return
	}
	id := c.restID()
	speeds.Lock()
	defer speeds.Unlock()
	r := speeds.m[id]
	if ttft > 0 {
		r.ttft, r.ttftW = fold(r.ttft, r.ttftW, now.Sub(r.at), float64(ttft))
		r.at = now
	}
	if tokens > 0 && w > 0 {
		if ttft > 0 {
			r.tps, r.tpsW = fold(r.tps, r.tpsW, now.Sub(r.tpsAt), float64(tokens)*1000/float64(w))
			r.tpsAt = now
		} else {
			r.whole, r.wholeW = fold(r.whole, r.wholeW, now.Sub(r.wholeAt), float64(tokens)*1000/float64(w))
			r.wholeAt = now
		}
	}
	if _, had := speeds.m[id]; !had && len(speeds.m) >= speedMost {
		for k, o := range speeds.m {
			if now.Sub(o.fresh()) > speedStale {
				delete(speeds.m, k)
			}
		}
	}
	speeds.m[id] = r
}

// noteTrySpeed is noteSpeed of a try that answered, its pace as
// usage.DecodeOf counts it for the Requests and Usage pages. A try that
// answered without streaming tells DecodeOf nothing — it has no first
// token for the window to start at — and its pace is its output over its
// whole wait, the reading the reporter gave the Requests page (#5575):
// coarser than a streamed one's, which counts the tokens after the first,
// kept apart from it, and enough to order the seat by.
func noteTrySpeed(c candidate, t Try, out, reasoning int, now time.Time) {
	tokens, w := 0, int64(0)
	if !t.ReasoningHidden || t.FirstText > 0 {
		tokens, w = usage.DecodeOf(out, reasoning, t.Millis, t.TTFT, t.FirstText, t.Flow, t.FirstReasoning)
		if tokens == 0 && w == 0 && t.TTFT <= 0 && out > 0 && t.Millis > 0 {
			tokens, w = out, t.Millis
		}
	}
	noteSpeed(c, t.TTFT, tokens, w, now)
}

// speedOf is c's speed lately: its average ms to the first token and
// tokens a second, 0 for what wasn't measured lately. The pace is the
// streamed average when one is fresh; only a seat whose fresh samples
// are all unstreamed is told by its whole-reply pace, and then with no
// first token (the review of #5575: the two readings aren't averaged
// together).
func speedOf(c candidate, now time.Time) (ttft, tps float64) {
	speeds.Lock()
	r, ok := speeds.m[c.restID()]
	speeds.Unlock()
	if !ok || now.Sub(r.fresh()) > speedStale {
		return 0, 0
	}
	if now.Sub(r.at) <= speedStale {
		ttft = r.ttft
	}
	if !r.tpsAt.IsZero() && now.Sub(r.tpsAt) <= speedStale {
		tps = r.tps
	}
	if tps == 0 && !r.wholeAt.IsZero() && now.Sub(r.wholeAt) <= speedStale {
		tps = r.whole
	}
	return ttft, tps
}

// forgetSpeeds drops every speed: a test starts anew.
func forgetSpeeds() {
	speeds.Lock()
	speeds.m = map[string]speedRec{}
	speeds.Unlock()
}
