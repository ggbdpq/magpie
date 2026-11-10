package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/provider"
)

// kimiSpentEmpty is Kimi Code's answer to every chat request once its
// plan's weekly window is spent (#5876, kedoupi): a 400 whose words say
// nothing of the window, in=0 in the usage.
const kimiSpentEmpty = `{"error":{"message":"text content is empty"}}`

// A routing group's Kimi Code member, its weekly window spent, answers
// every request that lands on it with a 400 "text content is empty"
// (#5876, kedoupi). Nothing of a quota's is in the words, so nothing
// read them: the 400 went to the agent, whose five reconnects met it
// again, and the turn kept on the member by its affinity sent it every
// request of the session, 25 of them in two hours. Read as the spent
// plan it is, the member rests as out of quota, and the group's next
// member answers; the next request goes past the resting member.
func TestKimiSpentEmpty400FailsOverAndRests(t *testing.T) {
	fresh(t)
	var mu sync.Mutex
	var seen []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		seen = append(seen, key)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if key == "k-spent" {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, kimiSpentEmpty)
			return
		}
		io.WriteString(w, `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"from-k-good"},"finish_reason":"stop"}],`+
			`"usage":{"prompt_tokens":5,"completion_tokens":3}}`)
	}))
	defer up.Close()
	for _, p := range []provider.Provider{
		{ID: "kimipool", Name: "Kimi Code (China)", Chat: up.URL + "/v1", Models: []string{"k3.1-pro"}, Key: "k-spent", KeyName: "Spent"},
		{ID: "otherpool", Name: "Other", Chat: up.URL + "/v1", Models: []string{"m2"}, Key: "k-good"},
	} {
		if err := provider.Save(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := provider.SaveGroup(provider.Group{Name: "G", Members: []string{"kimipool/k3.1-pro", "otherpool/m2"}, Routing: provider.Ordered}); err != nil {
		t.Fatal(err)
	}
	s := New()
	send := func() (int, string) {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
			strings.NewReader(`{"model":"group/g","messages":[{"role":"user","content":"hi"}]}`)))
		return rec.Code, rec.Body.String()
	}
	if code, body := send(); code != 200 || !strings.Contains(body, "from-k-good") {
		t.Fatalf("%d %s; tried %v", code, body, seen)
	}
	mu.Lock()
	if strings.Join(seen, ",") != "k-spent,k-good" {
		mu.Unlock()
		t.Fatalf("tried %v", seen)
	}
	seen = nil
	mu.Unlock()
	saved, err := provider.Find("kimipool")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := RestOf(saved.RestKey(saved.KeyList()[0])); !ok || r.Why != failQuota || r.Status != http.StatusBadRequest {
		t.Fatalf("the spent member's rest: %+v %v", r, ok)
	}
	if code, body := send(); code != 200 || !strings.Contains(body, "from-k-good") {
		t.Fatalf("next: %d %s", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(seen, ",") != "k-good" {
		t.Fatalf("next tried %v", seen)
	}
}
