package gateway

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
)

// What OpenCode's gateway answers a refusal with tells a short rate limit
// from a spent allowance — its own words, and how long it names — and what
// magpie said of a refusal was its status and the names of the headers
// sent, so a 429 at a third of the quota could not be told one from the
// other (#5509, lingengyuan). The refusal now says both, and puts the body
// back for what reads it next.
func TestOpenCodeRefusalSaysWhatTheVendorSaid(t *testing.T) {
	p := provider.Provider{ID: "opencode-go"}
	req := httptest.NewRequest("POST", "https://opencode.ai/zen/go/v1/messages", nil)
	req.Header.Set("x-opencode-session", "ses_e5031234abcd")
	body := `{"error":{"code":"rate_limited","message":"Too many requests. Please retry in a few seconds."}}`
	res := &http.Response{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": []string{"5"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })

	logOpenCodeRefusal(p, req, res)

	said := logs.String()
	for _, want := range []string{
		"answered 429 to POST opencode.ai/zen/go/v1/messages",
		"x-opencode-session ses_e503…",
		"retry-after 5s",
		"Too many requests. Please retry in a few seconds.",
	} {
		if !strings.Contains(said, want) {
			t.Fatalf("the refusal does not say %q: %s", want, said)
		}
	}
	rest, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(rest) != body {
		t.Fatalf("the body was not put back: %q", string(rest))
	}
}

// A refusal that names no wait and answers with a body that is no error
// object still says its own words, clipped, and no retry-after.
func TestOpenCodeRefusalSaysABareBodyAndNoWait(t *testing.T) {
	p := provider.Provider{ID: "opencode-go"}
	req := httptest.NewRequest("POST", "https://opencode.ai/zen/go/v1/messages", nil)
	res := &http.Response{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(`{"model":"deepseek-v4.1-flash"}`)),
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })

	logOpenCodeRefusal(p, req, res)

	said := logs.String()
	if strings.Contains(said, "retry-after") {
		t.Fatalf("no wait was named, yet the refusal said one: %s", said)
	}
	if !strings.Contains(said, `says "{\"model\":\"deepseek-v4.1-flash\"}"`) {
		t.Fatalf("the refusal does not say the body it refused: %s", said)
	}
	rest, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) == 0 {
		t.Fatal("the body was not put back")
	}
}

// A wait written as an HTTP date is said as how long it is from now, not
// as the date.
func TestOpenCodeRefusalSaysADatedWaitAsHowLong(t *testing.T) {
	p := provider.Provider{ID: "opencode-go"}
	req := httptest.NewRequest("POST", "https://opencode.ai/zen/go/v1/messages", nil)
	res := &http.Response{
		StatusCode: 429,
		Status:     "429 Too Many Requests",
		Header:     http.Header{"Retry-After": []string{time.Now().UTC().Add(90 * time.Second).Format(http.TimeFormat)}},
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"slow down"}}`)),
	}
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(old) })

	logOpenCodeRefusal(p, req, res)

	// said as how long from now, rounded to seconds — not as the date, and
	// with no sub-second tail
	if dated := regexp.MustCompile(`retry-after 1m\d+s`); !dated.MatchString(logs.String()) {
		t.Fatalf("the dated wait was not said as how long: %s", logs.String())
	}
}
