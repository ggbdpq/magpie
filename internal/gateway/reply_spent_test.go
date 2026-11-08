package gateway

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// What tianyi (eaichat.ctyun.cn) answers a spent plan with, the reporter's
// own reply short of its last clause (leavrcn, #5692): a 200, finish
// reason "stop", the usage counted, and no word of error in it.
const pitchBody = `{"model":"deepseek-v4-flash-0731-oc","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"您的Token额度已用完！当前套餐的额度已耗尽，如需继续使用，请前往 desk.ctyun.cn 购买或兑换套餐码。购买后，请新建会话。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":198,"total_tokens":207}}`

// A reply that only mentions the word of the plan, as an answer would.
const plainAnswer = `{"id":"cn","choices":[{"index":0,"message":{"role":"assistant","content":"您的额度还很充足，请放心使用。"},"finish_reason":"stop"}]}`

// scriptedOnWords registers a provider on its own fake upstream, with the
// words given as its failWords.
func scriptedOnWords(t *testing.T, id string, words []string, fallback []string, s *scripted) {
	t.Helper()
	up := httptest.NewServer(s)
	t.Cleanup(up.Close)
	p := provider.Provider{ID: id, Name: strings.ToUpper(id), Key: "k", Models: []string{"m"}, Chat: up.URL + "/v1", FailWords: words, Fallback: fallback}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
}

// A 200 that is only the vendor's own pitch for a spent plan fails as the
// words stand for it, and rests the account: an agent asking for JSON had
// the pitch for an answer, request after request, for as long as the plan
// was spent, with every page saying 200 (leavrcn, #5692). A member that
// can still answer is asked in its place.
func TestFailWordsFailAPitch(t *testing.T) {
	fresh(t)
	clineRestsCleared(t)
	old := allowances
	defer func() { allowances = old }()
	allowances = func(string) map[string]provider.Allowance { return nil }
	a := &scripted{replies: []reply{{200, "", pitchBody}}}
	b := &scripted{replies: []reply{{200, "", chatOK}}}
	scriptedOnWords(t, "a", []string{"额度已用完", `desk\.ctyun\.cn`}, []string{"b/m"}, a)
	scriptedOn(t, "b", provider.Chat, b)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") || a.n != 1 || b.n != 1 {
		t.Fatalf("%d %s (a asked %d times, b %d)", code, body, a.n, b.n)
	}
	if try := firstTry(t, s); try.Fail != failQuota || try.Rest == nil {
		t.Errorf("the pitch failed as %q (rest %v), want %q with a rest", try.Fail, try.Rest, failQuota)
	}
}

// With nobody left to ask, the pitch is told as the failure it stands for,
// not handed over as the answer it isn't: the agent has an error, never a
// completion that says the words.
func TestFailWordsToldWhenNobodyLeft(t *testing.T) {
	fresh(t)
	clineRestsCleared(t)
	a := &scripted{replies: []reply{{200, "", pitchBody}}}
	scriptedOnWords(t, "a", []string{"额度已用完"}, nil, a)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code < 400 || strings.Contains(body, `"choices"`) {
		t.Fatalf("%d %s", code, body)
	}
}

// Without the words the pitch goes as it always has: an answer, whatever
// it says. Nothing is read of a reply that the user hasn't opted in for.
func TestFailWordsOffByDefault(t *testing.T) {
	fresh(t)
	clineRestsCleared(t)
	a := &scripted{replies: []reply{{200, "", pitchBody}}}
	scriptedOn(t, "a", provider.Chat, a)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "额度已用完") || a.n != 1 {
		t.Fatalf("%d %s (asked %d times)", code, body, a.n)
	}
	if try := firstTry(t, s); try.Fail != "" || try.Rest != nil {
		t.Errorf("an answer the user opted nothing for failed as %q (rest %v)", try.Fail, try.Rest)
	}
}

// An answer that only mentions the word of the plan is spared: the words
// are matched as they are written, not as a guess of what a pitch says.
func TestFailWordsSpareAnAnswer(t *testing.T) {
	fresh(t)
	clineRestsCleared(t)
	a := &scripted{replies: []reply{{200, "", plainAnswer}}}
	scriptedOnWords(t, "a", []string{"额度已用完", "套餐已耗尽"}, nil, a)
	s := New()
	code, body := postAs(t, s, "", `{"model":"a/m","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "额度还很充足") || a.n != 1 {
		t.Fatalf("%d %s (asked %d times)", code, body, a.n)
	}
	if try := firstTry(t, s); try.Fail != "" || try.Rest != nil {
		t.Errorf("an answer that says none of the words failed as %q (rest %v)", try.Fail, try.Rest)
	}
}

// Each word stands for itself: one magpie can't read is dropped, and one
// it can is still matched. Nothing configured, nothing read at all.
func TestFailWordsRead(t *testing.T) {
	if re := failWordsOf(provider.Provider{}); re != nil {
		t.Errorf("nothing configured, %v is read of every reply", re)
	}
	if re := failWordsOf(provider.Provider{FailWords: []string{"  ", "", "("}}); re != nil {
		t.Errorf("words none of them readable: %v", re)
	}
	re := failWordsOf(provider.Provider{FailWords: []string{"额度已用完", " ( ", `desk\.ctyun\.cn`}})
	if re == nil {
		t.Fatal("a word magpie can read was dropped beside one it can't")
	}
	for _, said := range []string{"您的Token额度已用完！", "请前往 desk.ctyun.cn 购买"} {
		if !re.MatchString(said) {
			t.Errorf("%v doesn't match %q", re, said)
		}
	}
	if re.MatchString("您的额度还很充足，请放心使用") {
		t.Errorf("%v matched an answer that says none of the words", re)
	}
}

// failWords survive a save that left them out, as the editor's form
// doesn't carry them, and a save that sends none keeps what was stored.
func TestFailWordsKeptOnSave(t *testing.T) {
	fresh(t)
	p := provider.Provider{ID: "pitchy", Name: "Pitchy", Key: "k", Chat: "https://relay.example/v1", FailWords: []string{"额度已用完", "   "}}
	if err := provider.Save(p); err != nil {
		t.Fatal(err)
	}
	saved, err := provider.Find("pitchy")
	if err != nil || len(saved.FailWords) != 1 || saved.FailWords[0] != "额度已用完" {
		t.Fatalf("stored %v (%v)", saved.FailWords, err)
	}
	if err := provider.Save(provider.Provider{ID: "pitchy", Name: "Pitchy", Key: "k", Chat: "https://relay.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if saved, err = provider.Find("pitchy"); err != nil || len(saved.FailWords) != 1 {
		t.Errorf("a save that left failWords out dropped them: %v (%v)", saved.FailWords, err)
	}
}
