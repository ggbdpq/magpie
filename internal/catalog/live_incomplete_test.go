package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A body the link cut before it ended — a slow relay stops the transfer
// mid-string — is its own failure, and the message says so: what arrived
// is not a list of any shape, and pointing at the JSON's shape sends the
// user looking at the body when the body is fine, only short (#5457).
func TestFetchURLCutBodySaysIncomplete(t *testing.T) {
	full := `{"object":"list","data":[{"id":"m1"},{"id":"m2"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		// declared whole, sent cut, the connection closed: what a relay
		// that gives up mid-body leaves the reader with
		rw.Header().Set("Content-Length", strconv.Itoa(len(full)))
		rw.Write([]byte(full[:len(full)/2]))
	}))
	defer srv.Close()
	_, err := FetchURL(context.Background(), srv.URL, "", false, nil)
	if err == nil {
		t.Fatal("a cut body was taken for a model list")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("a cut body is not told from a body of the wrong shape: %v", err)
	}
	if strings.Contains(err.Error(), "not a model list") {
		t.Fatalf("a cut body is reported as the shape's failure: %v", err)
	}
}

// A body still arriving when the fetch's time is up is the same failure:
// the read is cut with the deadline, and what arrived is no list of any
// shape (#5457).
func TestFetchURLStalledBodySaysIncomplete(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.Write([]byte(`{"object":"list","data":[{"id":"m1"}`))
		rw.(http.Flusher).Flush()
		<-blockStalledBody // the tail never comes within the fetch's time
	}))
	defer srv.Close() // registered first, so it runs after the unblock
	defer close(blockStalledBody)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := FetchURL(ctx, srv.URL, "", false, nil)
	if err == nil {
		t.Fatal("a stalled body was taken for a model list")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("a stalled body is not told from a body of the wrong shape: %v", err)
	}
	if strings.Contains(err.Error(), "not a model list") {
		t.Fatalf("a stalled body is reported as the shape's failure: %v", err)
	}
}

var blockStalledBody = make(chan struct{})
