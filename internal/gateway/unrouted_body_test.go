package gateway

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// heatonz on Discord (Hermes Agent to magpie in Docker): requests held
// 2m0s "without routing it to any provider", the report's one wait "body
// 2m0s (under way)". A body that keeps coming, a byte at a time and never
// idle for the read's own limit, reached unroutedMost while it was still
// being read. It is answered 408, saying the body was still arriving and
// how much had come, and its read ends with the answer: the handler
// doesn't go on reading a body nobody will route.
func TestUnroutedTrickledBodyAnswered408(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	unroutedLimits(t, 20*time.Millisecond, 500*time.Millisecond)
	s, _ := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	// 40 bytes a second for 400 bytes: ten seconds, far past unroutedMost
	body := unroutedHi + strings.Repeat(" ", 400-len(unroutedHi))
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop) })
	go func() {
		for i := 0; i < len(body); i++ {
			select {
			case <-stop:
				return
			case <-time.After(25 * time.Millisecond):
			}
			if _, err := conn.Write([]byte{body[i]}); err != nil {
				return
			}
		}
	}()

	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	answered := time.Now()
	if res.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(b), "its body was still arriving") || !strings.Contains(string(b), "of 400 bytes, the last") {
		t.Fatalf("a body still arriving at unroutedMost was answered %d %s", res.StatusCode, b)
	}
	// the connection ends with the answer, long before the body would
	if _, err := br.ReadByte(); err != io.EOF && !isClosedConn(err) {
		t.Fatalf("after the 408 the connection read %v, not its end", err)
	}
	if waited := time.Since(answered); waited > 3*time.Second {
		t.Fatalf("the connection ended %v after the 408: its handler read on", waited)
	}

	// the model isn't read yet, so its route names none; the report is
	// saved after the answer
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		for _, r := range routesOf(s, "") {
			if r.Held == "" {
				continue
			}
			if r.Status != http.StatusRequestTimeout {
				t.Errorf("its route says %d", r.Status)
			}
			text, err := HeldReport(r.Held)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"stopped it with 408", "body read: ", "of 400 bytes, the last", "body still arriving"} {
				if !strings.Contains(text, want) {
					t.Errorf("the report doesn't say %q:\n%.1500s", want, text)
				}
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("no route names the report")
		}
	}
}

func isClosedConn(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "connection reset") || strings.Contains(err.Error(), "closed"))
}

// A full run caught the trickle above red with a 504 (#4511): the hold
// ran out between two of its reads, and the read's absence alone was
// taken for a hold of magpie's own. Less of the body having come than
// the request's own Content-Length says, the body is still arriving and
// the time is the client's, whether its handler was inside a read when
// the hold ran out or between two, or before its first. Here the hold
// runs out before the first read, in the wait for body room.
func TestUnroutedBodyShortOfItsLengthAnswered408(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	unroutedLimits(t, 20*time.Millisecond, 1500*time.Millisecond)
	// the room holds 2 MiB and this test holds 3 of it, so a request
	// with a 1 MiB body waits for room before its first read, and keeps
	// waiting while unroutedMost runs out
	wasRoom := bodyRoom
	bodyRoom = roomOf(2 << 20)
	t.Cleanup(func() { bodyRoom = wasRoom })
	hold, end := context.WithCancel(context.Background())
	defer end()
	if err := bodyRoom.take(hold, 3<<20); err != nil {
		t.Fatal(err)
	}

	s, _ := slowEyed(t, 0)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	// a body of a declared 1 MiB, none of it sent
	conn, err := net.Dial("tcp", strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(20 * time.Second))
	fmt.Fprintf(conn, "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", 1<<20)

	br := bufio.NewReader(conn)
	res, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusRequestTimeout || !strings.Contains(string(b), "its body was still arriving") || !strings.Contains(string(b), "read: none of 1.0 MB") {
		t.Fatalf("a body none of which had come was answered %d %s", res.StatusCode, b)
	}
	// the connection ends with the answer
	if _, err := br.ReadByte(); err != io.EOF && !isClosedConn(err) {
		t.Fatalf("after the 408 the connection read %v, not its end", err)
	}
}
