package gui

import (
	"net"
	"net/http"
	"testing"
	"time"
)

// #7612: Web.Close gives an in-flight response (such as POST /api/update/install's
// 204 No Content flushing over a slow link or proxy) a brief graceful Shutdown
// before force-closing, rather than tearing down the socket immediately.
func TestWebCloseFlushesInFlightResponse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			close(started)
			<-release
			rw.WriteHeader(http.StatusNoContent)
		}),
	}
	go srv.Serve(ln)
	w := &Web{srv: srv, quit: make(chan struct{})}

	type resp struct {
		code int
		err  error
	}
	got := make(chan resp, 1)
	go func() {
		res, err := (&http.Client{Timeout: 5 * time.Second}).Post("http://"+ln.Addr().String()+"/api/update/install", "application/json", nil)
		if err != nil {
			got <- resp{err: err}
			return
		}
		res.Body.Close()
		got <- resp{code: res.StatusCode}
	}()

	<-started
	closed := make(chan struct{})
	go func() {
		w.Close()
		close(closed)
	}()

	// Give Close a moment to begin Shutdown while the handler is still active,
	// then let the handler write its 204 response.
	time.Sleep(50 * time.Millisecond)
	close(release)

	r := <-got
	if r.err != nil || r.code != http.StatusNoContent {
		t.Fatalf("in-flight response cut off by Web.Close: code=%d err=%v", r.code, r.err)
	}
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Web.Close did not return after in-flight response finished")
	}
}
