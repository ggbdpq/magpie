package gateway

import "testing"

// The wordings mediaFetchRefusal is taken by, and its neighbours that
// stay none of its family. Protective only: green with the fix, and it
// cannot run without it, the words being the fix's own.
func TestMediaFetchRefusalWords(t *testing.T) {
	for b, want := range map[string]bool{
		mediaFetchRefusal: true,
		// the code alone, as a relay may pass the fields on apart
		`{"error":{"code":"media_fetch_failed","message":"failed"}}`: true,
		// a vendor's own wording, without the code
		`{"error":{"message":"The image URL could not be downloaded.","type":"invalid_request_error"}}`: true,
		// none of the family
		`{"error":{"message":"messages: field required"}}`:         false,
		`{"error":{"message":"Invalid tool schema"}}`:              false,
		`{"error":{"message":"Invalid API key"}}`:                  false,
		`{"error":{"message":"The reply could not be streamed."}}`: false,
	} {
		if got := mediaFetchWords.Match(wordsOf([]byte(b))); got != want {
			t.Errorf("mediaFetchWords(%s) = %v, want %v", b, got, want)
		}
		if got := shapeRefused(400, []byte(b)); got != want {
			t.Errorf("shapeRefused(400, %s) = %v, want %v", b, got, want)
		}
		if got := retryable(400, []byte(b)); got != want {
			t.Errorf("retryable(400, %s) = %v, want %v", b, got, want)
		}
	}
	// a 500 is retryable as it stands, and no shape refusal of this family
	if !retryable(500, []byte(mediaFetchRefusal)) || shapeRefused(500, []byte(mediaFetchRefusal)) {
		t.Fatal("a 500 is not this family's business")
	}
}
