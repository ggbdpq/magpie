package gateway

import (
	"encoding/base64"
	"strings"
	"testing"
)

// hardy-one (#6843): an images answer's usage was rewritten from three
// totals alone, and OpenAI's Images API names usage.input_tokens_details a
// member of that object — a client that reads it calls the draw a failure
// and throws the image away, though it was generated and billed. The
// vendor's own breakdown goes through as it said it, and a vendor that
// says none is given {"text_tokens": input, "image_tokens": 0}, so the
// member is always there.
func TestImagesUsageCarriesInputTokensDetails(t *testing.T) {
	s, e := easeled(t)
	b64 := base64.StdEncoding.EncodeToString(pngBytes)

	// the vendor's own breakdown goes through as it said it
	e.imagesReply = `{"created":1,"data":[{"b64_json":"` + b64 + `"}],"usage":{"input_tokens":14,"output_tokens":343,"input_tokens_details":{"text_tokens":10,"image_tokens":4}}}`
	code, a, raw := postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie","size":"1024x1024"}`)
	if code != 200 {
		t.Fatalf("code %d: %s", code, raw)
	}
	if a.Usage.InputTokensDetails == nil || a.Usage.InputTokensDetails.TextTokens != 10 || a.Usage.InputTokensDetails.ImageTokens != 4 {
		t.Errorf("the vendor's breakdown was dropped: %+v", a.Usage)
	}

	// a vendor that says none is given the member all the same
	e.imagesReply = `{"created":1,"data":[{"b64_json":"` + b64 + `"}],"usage":{"input_tokens":14,"output_tokens":343}}`
	_, a, raw = postImages(t, s, "/v1/images/generations", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie","size":"1024x1024"}`)
	if a.Usage.InputTokensDetails == nil || a.Usage.InputTokensDetails.TextTokens != 14 || a.Usage.InputTokensDetails.ImageTokens != 0 {
		t.Errorf("no breakdown was made one: %+v in %s", a.Usage, raw)
	}

	// edits as well
	e.imagesReply = `{"created":1,"data":[{"b64_json":"` + b64 + `"}],"usage":{"input_tokens":14,"output_tokens":343,"input_tokens_details":{"text_tokens":12,"image_tokens":2}}}`
	img := "data:image/png;base64," + b64
	_, a, raw = postImages(t, s, "/v1/images/edits", "application/json", `{"model":"art/gpt-image-1","prompt":"a magpie","images":[{"image_url":"`+img+`"}]}`)
	if a.Usage.InputTokensDetails == nil || a.Usage.InputTokensDetails.TextTokens != 12 {
		t.Errorf("edits dropped the breakdown: %+v in %s", a.Usage, raw)
	}
	if !strings.Contains(raw, `"input_tokens_details"`) {
		t.Errorf("the member isn't in the answer: %s", raw)
	}
}
