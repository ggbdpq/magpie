package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yetone/magpie/internal/provider"
	"github.com/yetone/magpie/internal/usage"
)

// A request asked twice — its first member overloaded, the next answering
// — goes to its ledger row with what its tries sent up to the vendor:
// UpBytes the bytes that went on the wire, tries summed, PlainBytes the
// last try's body before compression. Neither was in a row before (#4456),
// so a session's upstream traffic and what a hand-on re-sent could only be
// guessed at from outside.
func TestLedgerKeepsUpBytes(t *testing.T) {
	fresh(t)
	var sizes []int
	overloaded := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(b))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"overloaded"}}`)
	}))
	t.Cleanup(overloaded.Close)
	if err := provider.Save(provider.Provider{ID: "a", Name: "A", Key: "k", Models: []string{"m"}, Chat: overloaded.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		sizes = append(sizes, len(b))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, chatOK)
	}))
	t.Cleanup(ok.Close)
	if err := provider.Save(provider.Provider{ID: "b", Name: "B", Key: "k", Models: []string{"m"}, Chat: ok.URL + "/v1"}); err != nil {
		t.Fatal(err)
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"a/m", "b/m"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	code, body := postAs(t, s, "", `{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)
	if code != 200 || !strings.Contains(body, "hello") {
		t.Fatalf("%d %s", code, body)
	}
	if len(sizes) != 2 {
		t.Fatalf("the vendor saw %d asks (sizes %v)", len(sizes), sizes)
	}
	route := lastRoute(s).ID
	var rows []usage.Record
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows = rows[:0]
		for _, u := range usage.Load(time.Time{}) {
			if u.RouteID == route && !u.HandedOn {
				rows = append(rows, u)
			}
		}
		if len(rows) >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(rows) != 1 {
		t.Fatalf("the request's row: %d rows", len(rows))
	}
	row := rows[0]
	if row.Tries != 2 || row.Status != 200 {
		t.Fatalf("the request's row: tries %d, status %d", row.Tries, row.Status)
	}
	if row.UpBytes != sizes[0]+sizes[1] {
		t.Fatalf("UpBytes %d, the tries' bodies %v", row.UpBytes, sizes)
	}
	if row.PlainBytes != sizes[1] {
		t.Fatalf("PlainBytes %d, the last try's body %d", row.PlainBytes, sizes[1])
	}
}
