package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRemoteKeyIncompleteLimitIsUnknown(t *testing.T) {
	for _, body := range []struct {
		name, json string
	}{
		{"missing", `{"limited":true}`},
		{"null", `{"limited":true,"limit":null}`},
		{"empty", `{"limited":true,"limit":{}}`},
	} {
		t.Run(body.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != RemoteKeyLimitPath || r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("unexpected limit request: %s", r.URL.Path)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body.json))
			}))
			defer srv.Close()
			p := Provider{ID: "incomplete-" + body.name, Name: "Remote", Anthropic: srv.URL}
			q := remoteKeyCardOf(context.Background(), p, "key", "test-key", true)
			if q.Error == "" || q.GatewayKey != nil {
				t.Fatalf("incomplete limited reply displayed as known: error=%q key=%+v windows=%+v", q.Error, q.GatewayKey, q.Windows)
			}
		})
	}
}

func TestRemoteKeyIncompleteLimitKeepsLastReading(t *testing.T) {
	var reply atomic.Value
	reply.Store(`{"limited":true,"limit":{"tokenLimit":100,"tokens":25,"tokensLeft":75,"period":"day"}}`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(reply.Load().(string)))
	}))
	defer srv.Close()
	p := Provider{ID: "incomplete-last-reading", Name: "Remote", Anthropic: srv.URL}
	ctx := context.Background()
	first := remoteKeyCardOf(ctx, p, "key", "test-key", true)
	if first.GatewayKey == nil || !first.GatewayKey.Limited || len(first.Windows) != 1 || first.Windows[0].Used != 25 {
		t.Fatalf("valid limit not read: %+v", first)
	}
	reply.Store(`{"limited":true,"limit":{}}`)
	next := remoteKeyCardOf(ctx, p, "key", "test-key", true)
	if next.AsOf == nil || next.GatewayKey == nil || !next.GatewayKey.Limited || len(next.Windows) != 1 || next.Windows[0].Used != 25 {
		t.Fatalf("incomplete limit discarded the last reading: %+v", next)
	}
	reply.Store(`{"limited":false}`)
	last := remoteKeyCardOf(ctx, p, "key", "test-key", true)
	if last.AsOf != nil || last.GatewayKey == nil || last.GatewayKey.Limited || len(last.Windows) != 1 || !last.Windows[0].Unlimited {
		t.Fatalf("explicit unlimited reply changed: %+v", last)
	}
}
