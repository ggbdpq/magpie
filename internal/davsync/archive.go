package davsync

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/yetone/magpie/internal/settings"
)

// Bucket is the S3 sync's bucket for files other than the backup — the
// gateway's request archive — each at <prefix>/magpie/<name>, beside the
// backup, written as it is: no passphrase seals them, so they can be read
// in the bucket too. Past the size the archive on this computer grows to,
// the oldest calls in the bucket are removed the same way (#5558, a
// twenty-day bucket of 7,262 calls at 3.6 GiB that nothing ever removed).
type Bucket struct{ s *s3 }

// ErrNoObject is a name with nothing at it.
var ErrNoObject = errors.New("not in the bucket")

// S3Bucket is the bucket sync keeps the backup in; false when sync is off
// or to a WebDAV folder.
func S3Bucket() (*Bucket, bool) {
	c, ok := Load()
	if !ok || !c.S3() {
		return nil, false
	}
	s, err := newS3(c)
	if err != nil {
		return nil, false
	}
	return &Bucket{s}, true
}

// at is the client for name rather than the backup.
func (b *Bucket) at(name string) *s3 {
	s := *b.s
	s.key = strings.TrimSuffix(s.key, file) + strings.TrimLeft(name, "/")
	return &s
}

// Where names the bucket, its prefix and its server, as messages do.
func (b *Bucket) Where() string { return b.at("").where() }

// Put writes data at name, over what is there. Once it stands, the bucket
// is trimmed to its size, the oldest calls first.
func (b *Bucket) Put(ctx context.Context, name string, data []byte) error {
	s := b.at(name)
	res, err := s.send(ctx, http.MethodPut, data, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 64<<10))
		b.trimmed(ctx, int64(len(data)), s.key)
		return nil
	}
	return s.explain("write", readError(res))
}

// ArchiveCap is how large the request archive grows, in bytes, in the
// bucket as on this computer: settings' RequestArchiveLocalMB, a GiB
// unless set, at most a GiB.
func ArchiveCap() int64 {
	mb := settings.Load().RequestArchiveLocalMB
	if mb <= 0 {
		mb = 1024
	}
	return int64(min(mb, 1024)) << 20
}

// archived is one call of the bucket's archive.
type archived struct {
	key  string
	size int64
}

// capState is how much of the bucket's archive this process has put there
// since the last trim: from zero it is only an under-count, put right at
// the first trim past the size.
type capState struct {
	mu    sync.Mutex
	total int64
}

// capStates is each bucket's count, by its endpoint, bucket and prefix:
// S3Bucket hands out a new Bucket every time, so the count outlives it.
var capStates sync.Map

// root names a bucket's archive for its count, as the unconditional map
// names a server.
func (b *Bucket) root() string {
	return b.s.endpoint.String() + " " + b.s.bucket + " " + strings.TrimSuffix(b.s.key, file)
}

// trimmed counts n more bytes into the bucket's archive, and past its
// size lists the calls and removes the oldest until a tenth under it, as
// the archive on this computer is kept: the call just kept (kept) is
// never removed, though it alone be over the size. A listing that fails
// leaves the bucket as it is, for the next call to try again.
func (b *Bucket) trimmed(ctx context.Context, n int64, kept string) {
	st, _ := capStates.LoadOrStore(b.root(), &capState{})
	c := st.(*capState)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += n
	most := ArchiveCap()
	if c.total <= most {
		return
	}
	calls, err := b.calls(ctx)
	if err != nil {
		log.Printf("request archive: %s: not trimmed: %v", b.Where(), err)
		return
	}
	var total int64
	for _, k := range calls {
		total += k.size
	}
	goal := most - most/10
	for _, k := range calls {
		if total <= goal {
			break
		}
		if k.key == kept {
			continue
		}
		if err := b.s.del(ctx, k.key); err != nil {
			log.Printf("request archive: %s: not removed: %v", k.key, err)
			continue
		}
		total -= k.size
	}
	c.total = total
}

// calls is every call in the bucket's archive, oldest first: the listing
// comes by key, and a call's name starts with its date and time of day.
func (b *Bucket) calls(ctx context.Context) ([]archived, error) {
	prefix := strings.TrimSuffix(b.s.key, file) + "archive/"
	var out []archived
	token := ""
	seen := map[string]bool{}
	for range 100 {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		u := b.s.bucketURL()
		u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20")
		res, err := b.s.sendTo(ctx, http.MethodGet, u, nil, nil)
		if err != nil {
			return nil, err
		}
		if res.StatusCode != http.StatusOK {
			e := readError(res)
			res.Body.Close()
			return nil, b.s.explain("read", e)
		}
		var l struct {
			Contents []struct {
				Key  string `xml:"Key"`
				Size int64  `xml:"Size"`
			} `xml:"Contents"`
			Truncated bool   `xml:"IsTruncated"`
			Next      string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&l)
		res.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, c := range l.Contents {
			if strings.HasPrefix(c.Key, prefix) {
				out = append(out, archived{c.Key, c.Size})
			}
		}
		if !l.Truncated {
			return out, nil
		}
		if l.Next == "" || seen[l.Next] {
			return nil, fmt.Errorf("listing the request archive in the bucket: incomplete listing (missing or repeated continuation token)")
		}
		seen[l.Next] = true
		token = l.Next
	}
	return nil, fmt.Errorf("listing the request archive in the bucket: incomplete listing after 100 pages")
}

// Get reads what is at name, its first 16 MiB; ErrNoObject when there is
// nothing.
func (b *Bucket) Get(ctx context.Context, name string) ([]byte, error) {
	body, _, err := b.Open(ctx, name)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, 16<<20))
}

// Open is what is at name as it is read, however large, and its size (-1
// when the server doesn't say); ErrNoObject when there is nothing. The
// caller closes it.
func (b *Bucket) Open(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	s := b.at(name)
	res, err := s.send(ctx, http.MethodGet, nil, nil)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode != http.StatusOK {
		defer res.Body.Close()
		e := readError(res)
		if e.Status == http.StatusNotFound && e.Code != "NoSuchBucket" {
			return nil, 0, ErrNoObject
		}
		return nil, 0, s.explain("read", e)
	}
	return res.Body, res.ContentLength, nil
}
