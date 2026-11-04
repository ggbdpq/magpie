package redact

import (
	"strings"
	"testing"
)

// tianyk, #4087: 白名单, a common word, was taken for a name by the local
// model once, and from then on went as its placeholder everywhere, request
// after request (821 replacements in three days, "之前见过"): a value
// masked once is masked again wherever it stands (known.go), and the
// model's gate for an ordinary word (zhWordNotName, modelSpans) is asked
// no more. A value the reader reads as an ordinary word where it stands
// goes as written there, whatever first found it; a name the reader
// vouches for is masked again still.
func TestKnownWordTakenForANameGoesAsWritten(t *testing.T) {
	f := &fakeModel{values: map[string]string{"白名单": "private_person"}}
	o := Options{Personal: true, Model: f.find}
	if out, n := Mask("开启白名单之后，把要放行的域名填进去。", o); n != 0 {
		t.Fatalf("not left as written to begin with: %q", out)
	}
	// the next request, with the model finding nothing in it
	if out, n := Mask("白名单在设置里，加一行就行。", Options{Personal: true}); n != 0 {
		t.Errorf("as known: %q", out)
	}
	if out, n := Mask("设置里的白名单要加一行。", o); n != 0 {
		t.Errorf("as found again: %q", out)
	}

	// a name of its own, once masked, is masked again where no word stands
	// in its place: 欧阳娜娜 the reader takes for one (欧阳 a surname's
	// two characters, no word begins with them)
	fr := &fakeModel{values: map[string]string{"欧阳娜娜": "private_person"}}
	or := Options{Personal: true, Model: fr.find}
	if out, _ := Mask("客户欧阳娜娜来电话", or); strings.Contains(out, "欧阳娜娜") {
		t.Fatalf("not masked to begin with: %q", out)
	}
	if out, n := Mask("欧阳娜娜今天到了", Options{Personal: true}); n == 0 {
		t.Errorf("a known name left as written: %q", out)
	}
}
