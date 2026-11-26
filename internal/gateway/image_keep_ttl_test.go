package gateway

import (
	"strconv"
	"testing"
	"time"
)

// forgetRequestText must drop keptImages as well as the trace bodies and
// recent-calls ring so screenshots do not remain in memory when Show request
// content is turned off (#7634).
func TestForgetRequestTextClearsKeptImages(t *testing.T) {
	fresh(t)
	t.Cleanup(clearKeptImages)
	s := New()
	img := dsBigPNG(4<<10, "forget")
	id := keepImage([]byte(img))
	ref := strconv.FormatInt(id, 10)
	if got, ok := keptImageOf(ref); !ok || got != img {
		t.Fatalf("keptImageOf(%s) before forget = %v, want true", ref, ok)
	}
	s.forgetRequestText()
	if _, ok := keptImageOf(ref); ok {
		t.Fatalf("keptImageOf(%s) still kept after forgetRequestText", ref)
	}
	keptImages.Lock()
	n, sz := keptImages.lru.Len(), keptImages.size
	keptImages.Unlock()
	if n != 0 || sz != 0 {
		t.Fatalf("keptImages after forgetRequestText: len=%d size=%d, want 0, 0", n, sz)
	}
}

// Inline images in keptImages must expire automatically after keptImagesTTL
// of inactivity so an idle process releases its memory instead of holding
// up to keptImagesMax forever; reading an image refreshes its TTL (#7634).
func TestKeptImagesExpireAfterTTL(t *testing.T) {
	fresh(t)
	clearKeptImages()
	prev := keptImagesTTL
	keptImagesTTL = 90 * time.Millisecond
	t.Cleanup(func() {
		keptImagesTTL = prev
		clearKeptImages()
	})

	imgA := dsBigPNG(4<<10, "ttl-a")
	imgB := dsBigPNG(4<<10, "ttl-b")
	refA := strconv.FormatInt(keepImage([]byte(imgA)), 10)
	refB := strconv.FormatInt(keepImage([]byte(imgB)), 10)

	// Refresh B halfway through the TTL while leaving A untouched.
	time.Sleep(50 * time.Millisecond)
	if got, ok := keptImageOf(refB); !ok || got != imgB {
		t.Fatalf("keptImageOf(%s) mid-TTL = %v, want true", refB, ok)
	}

	// Wait past A's TTL (but within B's refreshed TTL): A must be gone while
	// B is still available.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		keptImages.Lock()
		_, hasA := keptImages.byID[mustInt64(t, refA)]
		keptImages.Unlock()
		if !hasA {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	keptImages.Lock()
	_, hasA := keptImages.byID[mustInt64(t, refA)]
	keptImages.Unlock()
	if hasA {
		t.Fatalf("untouched image %s still present after TTL", refA)
	}

	// Now let B expire automatically via the background one-shot timer
	// without calling keptImageOf, and verify cache size drops to 0 and
	// the timer stops.
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		keptImages.Lock()
		n, sz, tm := keptImages.lru.Len(), keptImages.size, keptImages.timer
		keptImages.Unlock()
		if n == 0 && sz == 0 && tm == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	keptImages.Lock()
	n, sz, tm := keptImages.lru.Len(), keptImages.size, keptImages.timer
	keptImages.Unlock()
	t.Fatalf("keptImages did not expire to empty: len=%d size=%d timer_nil=%v", n, sz, tm == nil)
}

func mustInt64(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
