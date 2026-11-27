package gateway

import (
	"strings"
	"testing"
)

// mediaFetchRefusal is Command Code's 400 for an image or video URL it
// could not fetch (#8023): the vendor's fetching of the media failed,
// not the request — the same conversation had carried the same image
// blocks through 11 earlier requests on the same member, every one a
// 200, and the URLs answered GET 200 all along.
const mediaFetchRefusal = `{"error":{"message":"An image or video URL could not be downloaded. It must be public, answer HTTP 200 within 30 seconds, and redirect at most 3 times.","type":"invalid_request_error","code":"media_fetch_failed","param":"messages"}}`

// A group member that cannot fetch the request's media had the turn end
// at itself: media_fetch_failed said nothing any of retryable's 400/422
// wordings match, so the group's second member, which may fetch the same
// URL its own way or take the picture as base64, was never asked — the
// same reasoning as zhipuImageRefusal (#4732). It is asked now, and
// nothing rests for the member, whose fetch was the only fault.
func TestMediaFetchRefusalFailsOver(t *testing.T) {
	plan := &fake{t: t, ctype: "application/json", code: 400, reply: mediaFetchRefusal}
	spare := &fake{t: t, ctype: "application/json", reply: `{"id":"from-spare","choices":[]}`}
	twoProviders(t, plan, spare)
	code, body := post(t, "/v1/chat/completions", chatReq)
	if code != 200 || !strings.Contains(body, "from-spare") {
		t.Fatalf("%d %s", code, body)
	}
	if plan.calls != 1 {
		t.Fatalf("plan asked %d times, want 1", plan.calls)
	}
}
