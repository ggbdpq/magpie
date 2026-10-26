package gateway

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/gwmw"
	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/settings"
)

// waitingMasker is masking that waits for the local model, which never
// answers: a pf-serve loading or busy with the requests ahead (#3682).
// let is closed when the test ends; left is closed when a wait let go
// because its request's context ended.
type waitingMasker struct{ let, left chan struct{} }

func (m waitingMasker) wait(ctx context.Context) {
	select {
	case <-ctx.Done():
		close(m.left)
		panic("stopped waiting for the local masking model")
	case <-m.let:
		panic("the test ended")
	}
}

func (m waitingMasker) JSONHitsContext(ctx context.Context, b []byte) ([]byte, []gwmw.Hit) {
	m.wait(ctx)
	return b, nil
}

func (m waitingMasker) TextHitsContext(ctx context.Context, s string) (string, []gwmw.Hit) {
	m.wait(ctx)
	return s, nil
}

func (m waitingMasker) JSONHits(b []byte) ([]byte, []gwmw.Hit) {
	return m.JSONHitsContext(context.Background(), b)
}

func (m waitingMasker) TextHits(s string) (string, []gwmw.Hit) {
	return m.TextHitsContext(context.Background(), s)
}
func (m waitingMasker) JSON(b []byte) ([]byte, int)   { b, _ = m.JSONHits(b); return b, 0 }
func (m waitingMasker) Text(s string) (string, int)   { s, _ = m.TextHits(s); return s, 0 }
func (waitingMasker) Restore(s string, _ bool) string { return s }
func (waitingMasker) Value(string) (string, bool)     { return "", false }
func (waitingMasker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) {
	return w, func() {}
}

// #3682: with Settings → Privacy's local model on, a request the model
// was still reading at 2 minutes was answered 504, its work thrown away,
// and so every time it was sent again (480 KB on an RX 470/580 takes the
// model 90s, and its turn waits for the requests ahead). Masking's time no
// longer counts toward unroutedMost: the request waits for the model,
// and its wait lets go when its client does, rather than keep its place
// in the model's queue.
func TestMaskingModelWaitNotCutOffAtMost(t *testing.T) {
	unroutedLimits(t, 20*time.Millisecond, 200*time.Millisecond)
	fresh(t)
	if err := settings.Save(settings.Settings{Redact: true}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Save(provider.Provider{ID: "probe", Name: "Probe", Chat: "http://127.0.0.1:9/v1", Key: "key", Models: []string{"text"}}); err != nil {
		t.Fatal(err)
	}
	on := modelOn
	modelOn = func() bool { return true }
	t.Cleanup(func() { modelOn = on })
	withoutMasker(t)
	m := waitingMasker{let: make(chan struct{}), left: make(chan struct{})}
	t.Cleanup(func() { close(m.let) })
	remove := gwmw.Mask(m)
	t.Cleanup(remove)

	s := New()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan unroutedReply, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, "POST", srv.URL+"/v1/chat/completions", strings.NewReader(`{"model":"probe/text","messages":[{"role":"user","content":"hi"}]}`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			got <- unroutedReply{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		got <- unroutedReply{code: resp.StatusCode, body: string(b)}
	}()
	u := watchedHold(t, s)
	if r := heldPast(t, u, 5*unroutedMost, got); r != nil {
		t.Fatalf("a request waiting for the masking model was answered %d %q %v past unroutedMost", r.code, r.body, r.err)
	}
	if u.masking.Load() == 0 {
		t.Fatal("held past unroutedMost, but not while masking")
	}
	cancel() // the client goes away
	select {
	case <-m.left:
	case <-time.After(10 * time.Second):
		t.Fatal("the request's wait for the model went on after its client went away")
	}
}

// A masking begun as the rest of a request's wait ran out still says it
// waited for the local masking model, what the model was doing and how to
// send without it; and no longer that what the model read is kept, which
// held only for texts it had finished.
func TestUnroutedMsgSaysTheMaskingModel(t *testing.T) {
	on, busy := modelOn, modelBusy
	modelOn, modelBusy = func() bool { return true }, func() string { return "it is still loading" }
	t.Cleanup(func() { modelOn, modelBusy = on, busy })
	m := unroutedMsg(2*time.Minute, true)
	if !strings.Contains(m, "waiting for the local masking model") || !strings.Contains(m, "it is still loading") ||
		!strings.Contains(m, "turn it off in Settings") || strings.Contains(m, "without routing") || strings.Contains(m, "kept") {
		t.Fatalf("held for the model: %s", m)
	}
}

// With the model off, a request held while masked says it was held while
// masking, not that it waited for a model that isn't on.
func TestUnroutedMsgNamesTheModelOnlyWhenItIsOn(t *testing.T) {
	on := modelOn
	modelOn = func() bool { return false }
	t.Cleanup(func() { modelOn = on })
	if m := unroutedMsg(2*time.Minute, true); strings.Contains(m, "model") || !strings.Contains(m, "while masking it") {
		t.Fatalf("model off: %s", m)
	}
	if m := unroutedMsg(2*time.Minute, false); !strings.Contains(m, "without routing it to any provider") {
		t.Fatalf("not masking: %s", m)
	}
}

// heldMasker is masking that is slow (the local model loading): every
// wait runs to its request's context's end and panics out, however many
// candidates ask — waitingMasker's one left channel a second wait would
// close again.
type heldMasker struct{ asked int }

func (m *heldMasker) wait(ctx context.Context) {
	m.asked++
	<-ctx.Done()
	panic("stopped waiting for the local masking model")
}

func (m *heldMasker) TextHitsContext(ctx context.Context, s string) (string, []gwmw.Hit) {
	m.wait(ctx)
	return s, nil
}

func (m *heldMasker) JSONHitsContext(ctx context.Context, b []byte) ([]byte, []gwmw.Hit) {
	m.wait(ctx)
	return b, nil
}

func (m *heldMasker) TextHits(s string) (string, []gwmw.Hit) {
	return m.TextHitsContext(context.Background(), s)
}

func (m *heldMasker) JSONHits(b []byte) ([]byte, []gwmw.Hit) {
	return m.JSONHitsContext(context.Background(), b)
}
func (m *heldMasker) JSON(b []byte) ([]byte, int)     { b, _ = m.JSONHits(b); return b, 0 }
func (m *heldMasker) Text(s string) (string, int)     { s, _ = m.TextHits(s); return s, 0 }
func (m *heldMasker) Restore(s string, _ bool) string { return s }
func (m *heldMasker) Value(string) (string, bool)     { return "", false }
func (m *heldMasker) Writer(w http.ResponseWriter) (http.ResponseWriter, func()) {
	return w, func() {}
}

// #3682: with the local masking model slow, a routing group's decision
// went candidate by candidate, each waiting out the classifier's whole
// timeout and resting its account with a 502 that was magpie's own
// holding, not the provider's: two candidates waited twice, and both
// accounts rested. A decision held for masking comes back at once, and
// no account rests for it.
func TestSlowMaskingRestsNoDecider(t *testing.T) {
	fresh(t)
	if err := settings.Save(settings.Settings{Redact: true, ClassifyTimeout: 1}); err != nil {
		t.Fatal(err)
	}
	was := classifyUnit
	classifyUnit = 100 * time.Millisecond // the 1 second set is 100ms
	t.Cleanup(func() { classifyUnit = was })
	withoutMasker(t)
	m := &heldMasker{}
	t.Cleanup(gwmw.Mask(m))

	s := New()
	cs := []candidate{
		{p: provider.Provider{ID: "p1", Key: "k1"}, model: "m1", rest: "p1#k1"},
		{p: provider.Provider{ID: "p2", Key: "k2"}, model: "m2", rest: "p2#k2"},
	}
	t0 := time.Now()
	_, err := s.askDecideTries(cs, []string{"debugging"}, before{}, false, "a message for the decider to read")
	if err == nil {
		t.Fatal("a decision held for masking comes back as an error")
	}
	var held maskingHeld
	if !errors.As(err, &held) {
		t.Fatalf("the error is not one masking held: %v", err)
	}
	if m.asked != 1 {
		t.Fatalf("the masker was asked %d times, want once: no other candidate is asked to wait the same out", m.asked)
	}
	if resting := restingNow(t); len(resting) != 0 {
		t.Fatalf("accounts rested for a masking hold: %v", resting)
	}
	if elapsed := time.Since(t0); elapsed > 3*classifyUnit {
		t.Fatalf("the hold waited %s, want about one timeout", elapsed)
	}
}
