package provider

import (
	"fmt"
)

// MaxBodyLimit is the most a provider's body limit may be set to. A
// vendor's own cap is far under it: Command Code takes 32 MiB in one
// request, a long Codex turn runs 45 MiB, and a GiB leaves room for
// either many times over.
const MaxBodyLimit = 1 << 30

// SetBodyLimit sets the most bytes one request to the provider may take,
// measured as it goes out (issue #4282): a body over it is refused
// before the provider is asked anything, and is not cut or sent again.
// 0, the default, takes the limit away.
func SetBodyLimit(id string, bytes int) error {
	if bytes < 0 || bytes > MaxBodyLimit {
		return fmt.Errorf("the body limit is from 0 (off) to %d bytes (a GiB), not %d", MaxBodyLimit, bytes)
	}
	p, err := Find(id)
	if err != nil {
		return err
	}
	p.BodyLimit = bytes
	return Save(*p)
}
