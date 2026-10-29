package redact

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A host's name the user@host rules find goes as one under a name set
// aside for it (RFC 2606's .example), not {{HOSTNAME_…}}: told "ssh to
// {{HOSTNAME_…}}", models asked the user for the real host instead of
// running ssh, as they did for an address before its documentation one
// (#4063). The model's ssh with the stand-in comes back with the real
// host, in a whole reply and a stream, and only a whole name is put back.
func TestHostGoesAsAStandInUnderExample(t *testing.T) {
	Forget()
	t.Cleanup(Forget)
	const host = "bastion.corp.acme.io"
	o := Options{Personal: true, Kinds: map[string]bool{"userhost": true}}
	msg := "run ssh rkato@" + host + " -p 22 and check the disk"
	out, n := Mask(msg, o)
	f := out[strings.Index(out, "@")+1:]
	f = f[:strings.IndexAny(f, " \t")]
	// the host goes as a name under .example, never a placeholder; the
	// user name beside it goes as its made-up name still
	if n != 2 || strings.Contains(out, "{{") || !strings.HasSuffix(f, ".example") ||
		len(strings.TrimSuffix(f, ".example")) != standInLen ||
		strings.Trim(strings.TrimSuffix(f, ".example"), "abcdefghijklmnopqrstuvwxyz") != "" {
		t.Fatalf("sent %q (host %q), want the host under .example and no placeholder", out, f)
	}
	if again, _ := Mask(msg, o); again != out {
		t.Errorf("not the same stand-in twice: %q, %q", out, again)
	}
	if p := Preview(msg, o); p != out {
		t.Errorf("preview %q, sent %q", p, out)
	}
	if back := Restore(out, false); back != msg {
		t.Errorf("put back as %q", back)
	}
	// the model's tool call, as it copied the command
	call := `{"command":"ssh rkato@` + f + ` -p 22 'uname -a; df -h'"}`
	if back := Restore(call, true); back != strings.Replace(call, f, host, 1) {
		t.Errorf("tool call put back as %s", back)
	}
	// only a whole name is put back
	for _, c := range []struct{ s, want string }{
		{f, host},
		{f + ":22", host + ":22"},
		{"ssh to " + f + ".", "ssh to " + host + "."},
		{"[" + f + "]", "[" + host + "]"},
		{"sub." + f, "sub." + f},
		{f + ".net", f + ".net"},
		{f + ".5", f + ".5"},
		{f + "x", f + "x"},
		{"1" + f, "1" + f},
	} {
		if got := Restore(c.s, false); got != c.want {
			t.Errorf("Restore(%q) = %q, want %q", c.s, got, c.want)
		}
	}

	// streamed: the tool call's arguments cut at every place in the name
	whole := `{"command":"ssh -p 22 root@` + f + ` 'uname -a'"}`
	for cut := strings.Index(whole, f); cut <= strings.Index(whole, f)+len(f); cut++ {
		ev := func(args string) string {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{
				"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": args}}}}}}})
			return "data: " + string(b) + "\n\n"
		}
		rec := httptest.NewRecorder()
		rec.Header().Set("Content-Type", "text/event-stream")
		w := NewWriter(rec)
		w.WriteHeader(200)
		for _, e := range []string{ev(whole[:cut]), ev(whole[cut:]), "data: [DONE]\n\n"} {
			w.Write([]byte(e))
		}
		w.Finish()
		var args strings.Builder
		for _, e := range strings.Split(rec.Body.String(), "\n\n") {
			d, ok := strings.CutPrefix(e, "data: ")
			if !ok || d == "[DONE]" {
				continue
			}
			var x struct {
				Choices []struct {
					Delta struct {
						ToolCalls []struct{ Function struct{ Arguments string } } `json:"tool_calls"`
					}
				}
			}
			if err := json.Unmarshal([]byte(d), &x); err != nil {
				t.Fatalf("event %q: %v", d, err)
			}
			for _, c := range x.Choices[0].Delta.ToolCalls {
				args.WriteString(c.Function.Arguments)
			}
		}
		if want := strings.Replace(whole, f, host, 1); args.String() != want {
			t.Errorf("cut at %d: arguments %s, want %s", cut, args.String(), want)
		}
	}

	// a stand-in is never masked again: no host ends in .example
	if again, n := Mask("ping "+f, o); n != 0 || again != "ping "+f {
		t.Errorf("a stand-in masked again as %q", again)
	}
}

// A host on a line of its own (Hostname:, ssh config's HostName) and one
// in a shell prompt go as the stand-in too.
func TestOtherHostRulesGoAsAStandIn(t *testing.T) {
	Forget()
	t.Cleanup(Forget)
	o := Options{Personal: true, Kinds: map[string]bool{"userhost": true}}
	for _, c := range []struct{ in, host string }{
		{"Hostname: rina-mbp.local\n", "rina-mbp.local"},
		{"Host prod\n  HostName box17.acme.io\n  User rkato\n", "box17.acme.io"},
		{"rinak@rina-mbp:~/src$ ls", "rina-mbp"},
	} {
		out, n := Mask(c.in, o)
		if !strings.Contains(out, ".example") || strings.Contains(out, "{{") {
			t.Errorf("%q: sent %q, want the host under .example", c.in, out)
		}
		if back := Restore(out, false); back != c.in {
			t.Errorf("%q: round trip → %q (%d masked)", c.in, back, n)
		}
	}
}
