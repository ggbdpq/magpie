package gateway

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/yetone/magpie/internal/provider"
)

// spentPitch is what a reply a provider's failWords match is failed as: a
// relay that has spent its plan answering a 200 with a pitch of its own is
// asked no more, this try's rest saying what the words stand for, as the
// vendor's own error status would (leavrcn, #5692).
const spentPitch = "the provider answered with words of a plan spent (failWords)"

// spentRunes is how much of what the vendor actually said a failure of
// this kind carries, for the agent to see the pitch it would have had.
const spentRunes = 300

// failWordsOf is a provider's failWords as the words they are matched by,
// each standing for itself: one magpie can't read is dropped, not fatal —
// a word mistyped mustn't fail every reply the provider answers. nil when
// none is configured, or none is left: nothing is read of a reply then,
// a 200 being an answer, whatever it says.
func failWordsOf(p provider.Provider) *regexp.Regexp {
	var good []string
	for _, w := range p.FailWords {
		if w = strings.TrimSpace(w); w == "" {
			continue
		}
		if _, err := regexp.Compile(w); err != nil {
			continue
		}
		good = append(good, w)
	}
	if len(good) == 0 {
		return nil
	}
	re, err := regexp.Compile(`(?i)(?:` + strings.Join(good, `|`) + `)`)
	if err != nil {
		return nil
	}
	return re
}

// pitchSaid is whether a reply that came whole under a success status says
// one of the provider's failWords, which the user opted in for: the words
// are read as they are written, never as a guess of what a pitch says
// (#5089's rule — an answer that carries one of the words of the language
// alone is an answer still). The status is what the reply's own words
// stand for (streamStatus), as the same reading of an error said under a
// 200 is (errorReply), and msg carries the pitch itself, short.
func pitchSaid(re *regexp.Regexp, body []byte) (status int, msg string, ok bool) {
	if re == nil {
		return 0, "", false
	}
	w := wordsOf(body)
	if hit := re.Find(w); hit == nil {
		return 0, "", false
	}
	said := pitchOf(body)
	if r := []rune(said); len(r) > spentRunes {
		said = string(r[:spentRunes])
	}
	return streamStatus(string(w)), fmt.Sprintf("%s: %s", spentPitch, said), true
}

// pitchOf is what a reply actually says, in whichever of the four shapes
// it came, for a failure to carry the words rather than their JSON: the
// body's own words when none of the shapes has it.
func pitchOf(body []byte) string {
	for _, at := range []string{
		"choices.0.message.content", "choices.0.delta.content",
		"content", "content.0.text",
		"output_text", "output.0.content.0.text",
		"candidates.0.content.parts.0.text",
	} {
		if s := gjson.GetBytes(body, at); s.Type == gjson.String && strings.TrimSpace(s.String()) != "" {
			return s.String()
		}
	}
	return string(wordsOf(body))
}
