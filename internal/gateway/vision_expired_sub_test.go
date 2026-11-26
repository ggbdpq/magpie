package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/yetone/magpie/internal/catalog"
	"github.com/yetone/magpie/internal/provider"
)

const (
	arkExpiredSub400   = `{"error":{"code":"InvalidSubscription","message":"Your account (2100000000) does not have a valid CodingPlan subscription, or your subscription has expired."}}`
	arkNoCodingPlan404 = `{"error":{"code":"InvalidEndpointOrModel.NotFound","message":"The requested model does not support the coding plan feature."}}`
)

func holdVisionProvider(t *testing.T, pid string) {
	t.Helper()
	heldPicks.Lock()
	writeAutoPicks(autoPicksFile{Seeded: true, Vision: pid})
	heldPicks.Unlock()
}

func TestExpiredSubscriptionClassifiedAsCreditAndRetryable(t *testing.T) {
	for _, body := range []string{
		arkExpiredSub400,
		`{"error":{"message":"No active subscription found for this account."}}`,
		`{"error":{"message":"当前账户订阅已过期，请续费后重试"}}`,
	} {
		if f := failure(http.StatusBadRequest, []byte(body)); f != failCredit {
			t.Errorf("failure(400, %s) = %q, want %q", body, f, failCredit)
		}
		if !retryable(http.StatusBadRequest, []byte(body)) {
			t.Errorf("retryable(400, %s) = false, want true", body)
		}
		if shapeRefused(http.StatusBadRequest, []byte(body)) {
			t.Errorf("shapeRefused(400, %s) = true, want false", body)
		}
	}

	plain := `{"error":{"message":"Invalid parameter: subscription_id"}}`
	if f := failure(http.StatusBadRequest, []byte(plain)); f != failOther {
		t.Errorf("plain 400 classified as %q, want %q", f, failOther)
	}
	if retryable(http.StatusBadRequest, []byte(plain)) {
		t.Errorf("plain 400 retryable = true, want false")
	}

	if !modelRefused(http.StatusNotFound, []byte(arkNoCodingPlan404)) {
		t.Errorf("modelRefused(404, arkNoCodingPlan404) = false, want true")
	}
	if modelRefused(http.StatusBadRequest, []byte(`{"error":{"message":"This model does not support image input."}}`)) {
		t.Errorf("image refusal wrongly classified as modelRefused")
	}
}

func TestAutoVisionExpiredSubscriptionRestsAndFallsBackToHealthyProvider(t *testing.T) {
	fresh(t)

	// Text-only target model on its own provider.
	textUp := &eyeUp{}
	textSrv := httptest.NewServer(textUp)
	t.Cleanup(textSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "probe", Name: "Probe", Chat: textSrv.URL + "/v1", Key: "probe-key", Models: []string{"text"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", textSrv.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
	}); err != nil {
		t.Fatal(err)
	}

	// Expired CodingPlan provider with 3 vision models, held as Automatic vision pick.
	var mu sync.Mutex
	var arkCalls []string
	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m := modelOf(body)
		mu.Lock()
		arkCalls = append(arkCalls, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if m == "minimax-m3" {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, arkNoCodingPlan404)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, arkExpiredSub400)
	}))
	t.Cleanup(arkSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "ark", Name: "Volcengine Ark", Chat: arkSrv.URL + "/v1", Key: "ark-key",
		Models: []string{"glm-5.3-flash", "deepseek-v4.1-flash", "minimax-m3"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("ark", arkSrv.URL+"/v1", []catalog.Model{
		{ID: "glm-5.3-flash", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.1, Output: 0.1}},
		{ID: "deepseek-v4.1-flash", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.2, Output: 0.2}},
		{ID: "minimax-m3", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.3, Output: 0.3}},
	}); err != nil {
		t.Fatal(err)
	}

	// Healthy fallback provider with a more expensive vision model.
	var loomyCalls []string
	loomySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		loomyCalls = append(loomyCalls, modelOf(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"A screenshot from loomy."},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(loomySrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "loomy", Name: "Loomy", Chat: loomySrv.URL + "/v1", Key: "loomy-key",
		Models: []string{"glm-5.3-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("loomy", loomySrv.URL+"/v1", []catalog.Model{
		{ID: "glm-5.3-flash", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 1.0, Output: 1.0}},
	}); err != nil {
		t.Fatal(err)
	}

	holdVisionProvider(t, "ark")
	s := New()

	// Image 1: ark fails once with expired subscription 400, rests as failCredit,
	// and describeWith falls back immediately to loomy without trying ark's other models.
	code, body := postAs(t, s, "", pastedImage)
	if code != 200 {
		t.Fatalf("image 1: %d %s (ark calls: %v, loomy calls: %v)", code, body, arkCalls, loomyCalls)
	}
	mu.Lock()
	gotArk1 := append([]string(nil), arkCalls...)
	gotLoomy1 := append([]string(nil), loomyCalls...)
	mu.Unlock()
	if len(gotArk1) != 1 {
		t.Fatalf("image 1 asked expired ark %d times (%v), want 1", len(gotArk1), gotArk1)
	}
	if len(gotLoomy1) != 1 {
		t.Fatalf("image 1 asked loomy %d times (%v), want 1", len(gotLoomy1), gotLoomy1)
	}
	if got := textUp.sent("text"); len(got) != 1 || !strings.Contains(got[0], "loomy/glm-5.3-flash describes it") {
		t.Fatalf("text model got %v", got)
	}
	arkP, err := provider.Find("ark")
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := RestOf(arkP.RestKey(arkP.KeyList()[0])); !ok || r.Why != failCredit {
		t.Fatalf("ark rest = %+v (ok=%v), want Why=%q", r, ok, failCredit)
	}

	// Image 2 (distinct image): AutoVision must skip resting ark completely and go straight to loomy.
	img2 := strings.Replace(pastedImage, "aGVsbG8=", "d29ybGQ=", 1)
	code, body = postAs(t, s, "", img2)
	if code != 200 {
		t.Fatalf("image 2: %d %s", code, body)
	}
	mu.Lock()
	gotArk2 := append([]string(nil), arkCalls...)
	gotLoomy2 := append([]string(nil), loomyCalls...)
	mu.Unlock()
	if len(gotArk2) != 1 {
		t.Fatalf("image 2 hit resting ark again: total ark calls %v", gotArk2)
	}
	if len(gotLoomy2) != 2 {
		t.Fatalf("image 2 loomy calls %v, want 2", gotLoomy2)
	}
}

func TestAutoVisionUnserved404ModelSkippedWithoutRestingSiblings(t *testing.T) {
	fresh(t)

	textUp := &eyeUp{}
	textSrv := httptest.NewServer(textUp)
	t.Cleanup(textSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "probe", Name: "Probe", Chat: textSrv.URL + "/v1", Key: "probe-key", Models: []string{"text"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", textSrv.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var calls []string
	arkSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		m := modelOf(body)
		mu.Lock()
		calls = append(calls, m)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if m == "minimax-m3" {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, arkNoCodingPlan404)
			return
		}
		io.WriteString(w, `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"Described by sibling."},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(arkSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "ark", Name: "Volcengine Ark", Chat: arkSrv.URL + "/v1", Key: "ark-key",
		Models: []string{"minimax-m3", "glm-5.3-flash"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("ark", arkSrv.URL+"/v1", []catalog.Model{
		{ID: "minimax-m3", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.1, Output: 0.1}},
		{ID: "glm-5.3-flash", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.5, Output: 0.5}},
	}); err != nil {
		t.Fatal(err)
	}

	s := New()
	if code, body := postAs(t, s, "", pastedImage); code != 200 {
		t.Fatalf("image 1: %d %s (calls %v)", code, body, calls)
	}
	// Image 2: minimax-m3 rests alone as unserved, so AutoVision goes directly to glm-5.3-flash.
	img2 := strings.Replace(pastedImage, "aGVsbG8=", "d29ybGQ=", 1)
	if code, body := postAs(t, s, "", img2); code != 200 {
		t.Fatalf("image 2: %d %s (calls %v)", code, body, calls)
	}
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if strings.Join(got, ",") != "minimax-m3,glm-5.3-flash,glm-5.3-flash" {
		t.Fatalf("calls = %v, want minimax-m3 once then glm-5.3-flash twice", got)
	}
}

func TestDescribeWithTriesAnotherProviderBeforeMoreModelsOnFailingProvider(t *testing.T) {
	fresh(t)

	textUp := &eyeUp{}
	textSrv := httptest.NewServer(textUp)
	t.Cleanup(textSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "probe", Name: "Probe", Chat: textSrv.URL + "/v1", Key: "probe-key", Models: []string{"text"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("probe", textSrv.URL+"/v1", []catalog.Model{
		{ID: "text", ImageInput: imageInputBool(false)},
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var badModels, goodModels []string
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		badModels = append(badModels, modelOf(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"message":"upstream exploded"}}`)
	}))
	t.Cleanup(badSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "bad", Name: "Bad", Chat: badSrv.URL + "/v1", Key: "bad-key",
		Models: []string{"m1", "m2", "m3"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("bad", badSrv.URL+"/v1", []catalog.Model{
		{ID: "m1", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.1, Output: 0.1}},
		{ID: "m2", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.2, Output: 0.2}},
		{ID: "m3", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 0.3, Output: 0.3}},
	}); err != nil {
		t.Fatal(err)
	}

	goodSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		goodModels = append(goodModels, modelOf(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"ok","choices":[{"index":0,"message":{"role":"assistant","content":"Described by good."},"finish_reason":"stop"}]}`)
	}))
	t.Cleanup(goodSrv.Close)
	if err := provider.Save(provider.Provider{
		ID: "good", Name: "Good", Chat: goodSrv.URL + "/v1", Key: "good-key",
		Models: []string{"eye"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := catalog.SaveLive("good", goodSrv.URL+"/v1", []catalog.Model{
		{ID: "eye", Images: true, ImageInput: imageInputBool(true), Price: &catalog.Price{Input: 5.0, Output: 5.0}},
	}); err != nil {
		t.Fatal(err)
	}

	holdVisionProvider(t, "bad")
	s := New()
	code, body := postAs(t, s, "", pastedImage)
	if code != 200 {
		t.Fatalf("expected fallback to good/eye: %d %s (bad=%v, good=%v)", code, body, badModels, goodModels)
	}
	mu.Lock()
	gotBad := append([]string(nil), badModels...)
	gotGood := append([]string(nil), goodModels...)
	mu.Unlock()
	for _, m := range gotBad {
		if m != "m1" {
			t.Fatalf("describeWith tried another model %q on already-failed provider before trying good: bad=%v good=%v", m, gotBad, gotGood)
		}
	}
	if len(gotGood) != 1 || gotGood[0] != "eye" {
		t.Fatalf("good calls = %v, want [eye]", gotGood)
	}
}
