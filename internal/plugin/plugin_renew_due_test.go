package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A renewal that leaves the sign-in still due isn't renewed again by the
// next thing to use the account: a plugin with an hour's refreshLead
// that, unable to renew, gives back only an expires half an hour out (or
// a new token with one, or nothing at all) had every listing, request and
// keep-alive renew the same sign-in again at once, and each save told
// magpie the sign-ins changed, which lists every provider again — the
// vendor of every plugin was asked every few seconds for as long as
// magpie ran. A renewal like that is held back RENEW_RETRY as a failed
// one is; one that brought no new token isn't saved, as it renewed
// nothing; the keep-alive is told why it couldn't, so it backs off.
func TestRenewalStillDueBacksOff(t *testing.T) {
	sandbox(t)
	logf := filepath.Join(t.TempDir(), "renewals")
	t.Setenv("RENEW_LOG", logf)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	abs, _ := filepath.Abs("testdata/renew-due/index.js")
	if _, err := Add(ctx, abs); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(time.Minute).UnixMilli() // inside the hour's lead
	account := func(who, refresh string) string {
		t.Helper()
		k, err := Import(ctx, "dueco", map[string]any{"type": "oauth", "refresh": refresh, "access": "old-" + who, "expires": soon, "accountId": who})
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	keep := account("keep@due", "d-keep")
	fresh := account("new@due", "d-new")
	none := account("none@due", "d-none")
	stored := func(key string) map[string]any {
		var all map[string]map[string]any
		b, _ := os.ReadFile(AuthPath())
		json.Unmarshal(b, &all)
		return all[key]
	}
	renewals := func(prefix string) int {
		b, _ := os.ReadFile(logf)
		n := 0
		for _, l := range strings.Fields(string(b)) {
			if strings.HasPrefix(l, prefix) {
				n++
			}
		}
		return n
	}

	// one listing renews each due sign-in once, before its models hook
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	first := time.Now()
	for _, p := range []string{"d-keep", "d-new", "d-none"} {
		if n := renewals(p); n != 1 {
			t.Fatalf("%s renewed %d times by one listing, want 1", p, n)
		}
	}
	// the next listing, a moment later, renews none of them again; nor
	// does the keep-alive that asks right after it
	if _, err := Providers(ctx); err != nil {
		t.Fatal(err)
	}
	k, err := KeepSignedIn(ctx, "dueco", keep)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(first) < 20*time.Second { // RENEW_RETRY is 30s
		for _, p := range []string{"d-keep", "d-new", "d-none"} {
			if n := renewals(p); n != 1 {
				t.Errorf("%s renewed %d times: a renewal that left the sign-in due was made again at once", p, n)
			}
		}
		// the keep-alive is told why, as from a renewal that failed
		if !k.Due || k.Renewed || !k.Waiting || !strings.Contains(k.Err, "refreshLead") {
			t.Errorf("keep-alive on the still-due sign-in: %+v", k)
		}
	} else {
		t.Logf("the two listings took %s, past RENEW_RETRY: renewing again is right", time.Since(first))
	}

	// a renewal that brought no new token saved nothing: the sign-in as
	// it was
	if a := stored(keep); a["access"] != "old-keep@due" || a["refresh"] != "d-keep" || int64(a["expires"].(float64)) != soon {
		t.Errorf("a renewal with no new token was saved: %v", a)
	}
	// a new token, though its expires is still inside the lead: kept
	if a := stored(fresh); a["access"] != "old-new@due+" || a["refresh"] != "d-new+" {
		t.Errorf("a renewal's new token wasn't kept: %v", a)
	}
	// nothing given back: the sign-in as it was
	if a := stored(none); a["access"] != "old-none@due" || int64(a["expires"].(float64)) != soon {
		t.Errorf("a renewal that gave back nothing changed the sign-in: %v", a)
	}
}
