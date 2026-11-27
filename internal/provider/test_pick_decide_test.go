package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// A decision model's test is pinned like any other's (#8006): the pin
// answered before the decision branch returned, so a decide-only
// provider's probe went out with routing's first key whatever the user
// picked, and the keys of a multi-key decision provider could not be
// told apart (14 of 15 AI Studio keys at quota 0, and no way to test
// which one worked).
func TestModelTestPinnedToAKeyOnADecisionModel(t *testing.T) {
	probeHome(t)
	var mu sync.Mutex
	var askedLog []string
	asked := func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), askedLog...) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/systemone") {
			http.NotFound(w, r)
			return
		}
		var q struct{ Model string }
		json.NewDecoder(r.Body).Decode(&q)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		askedLog = append(askedLog, key+":"+q.Model)
		mu.Unlock()
		if key != "sk-b" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"bad key"}}`)
			return
		}
		w.Write([]byte(`{"answers":{"ok":{"answer":true}}}`))
	}))
	defer srv.Close()
	if err := Save(Provider{ID: "decpool", Name: "DecPool", Decide: srv.URL + "/v1",
		Key: "sk-a", KeyName: "first", Keys: []KeyAccount{{Name: "second", Key: "sk-b"}}, Models: []string{"d1"}}); err != nil {
		t.Fatal(err)
	}
	p, err := Find("decpool")
	if err != nil {
		t.Fatal(err)
	}
	picks := p.TestPicks()
	if len(picks) != 2 || picks[0].Name != "first" || picks[1].Name != "second" {
		t.Fatalf("the picks: %+v", picks)
	}
	// pinned to the second key: only it is asked, and the result says so
	rs := p.TestModels(TestOn(context.Background(), keyID("sk-b")), []string{"d1"})
	if len(rs) != 1 || !rs[0].OK || rs[0].Protocol != "decide" || rs[0].Account != "second" {
		t.Fatalf("pinned to the second key: %+v (asked %v)", rs, asked())
	}
	if got := asked(); len(got) != 1 || !strings.HasPrefix(got[0], "sk-b:") {
		t.Fatalf("the pick was asked %v, want sk-b alone", got)
	}
	// pinned to the first: it is asked, and says so failing
	mu.Lock()
	askedLog = nil
	mu.Unlock()
	rs = p.TestModels(TestOn(context.Background(), keyID("sk-a")), []string{"d1"})
	if len(rs) != 1 || rs[0].OK || rs[0].Account != "first" || rs[0].Error == "" {
		t.Fatalf("pinned to the first key: %+v (asked %v)", rs, asked())
	}
	if got := asked(); len(got) != 1 || !strings.HasPrefix(got[0], "sk-a:") {
		t.Fatalf("the pick was asked %v, want sk-a alone", got)
	}
	// nothing pinned: routing's first, still named
	rs = p.TestModels(context.Background(), []string{"d1"})
	if len(rs) != 1 || rs[0].OK || rs[0].Account != "first" {
		t.Fatalf("unpinned: %+v", rs)
	}
}
