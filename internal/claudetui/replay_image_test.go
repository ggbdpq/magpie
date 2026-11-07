package claudetui

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// A replayed history whose words hold a line naming an image file loses
// those words when an image follows: 2.1.318's paste handler tries each
// pasted line as an image file before it takes the words in, so they are
// taken in late, and the end of the image path pasted next ends that
// take-in — the words land after the Enter, one turn late (#5548, Suge8).
// send waits for every text piece to show before it pastes the next, so
// the replayed words go out with their image.
func TestShimReplayKeepsItsTextBeforeAnImage(t *testing.T) {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	history := "The codeword is MANGO.\nSome notes.\nfoo.png\nmore notes."
	msg := `[{"type":"text","text":` + strconvQuote(history) + `},` +
		`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64("red") + `"}},` +
		`{"type":"text","text":"(The images above, then the codeword.)"}]`
	out := runShim(t, []string{"FAKE_SLOW_PASTE=1"}, append([]string{fakePath(t)}, pArgs...), []string{msg}, 2)
	said, _ := json.Marshal(out[1])
	if !strings.Contains(string(said), "MANGO") {
		t.Errorf("the replayed history's words missed their turn: %s", said)
	}
}
