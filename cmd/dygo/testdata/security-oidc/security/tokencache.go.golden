package security

import (
	"hash/maphash"
	"sync"
	"time"
)

// tokenCache remembers tokens that passed every check, until they expire, so
// a client that sends the same token on every request pays for the
// signature check once. Only verified tokens go in, keyed by the whole
// token, and an entry is dropped when the token expires. The Principal is
// shared between requests: treat its slices and Claims as read-only.
type tokenCache struct {
	seed   maphash.Seed
	shards [16]tokenShard
}

type tokenShard struct {
	mu sync.Mutex
	m  map[uint64]cachedToken
}

type cachedToken struct {
	token string
	p     Principal
	until time.Time
}

// tokensPerShard bounds the cache: 16 shards of 1024 tokens.
const tokensPerShard = 1024

func newTokenCache() *tokenCache {
	c := &tokenCache{seed: maphash.MakeSeed()}
	for i := range c.shards {
		c.shards[i].m = map[uint64]cachedToken{}
	}
	return c
}

func (c *tokenCache) get(token string, now time.Time) (Principal, bool) {
	h := maphash.String(c.seed, token)
	s := &c.shards[h%uint64(len(c.shards))]
	s.mu.Lock()
	e, ok := s.m[h]
	s.mu.Unlock()
	if !ok || e.token != token || !now.Before(e.until) {
		return Principal{}, false
	}
	return e.p, true
}

// put remembers token until until. A full shard first drops expired
// tokens, then any one.
func (c *tokenCache) put(token string, p Principal, until, now time.Time) {
	h := maphash.String(c.seed, token)
	s := &c.shards[h%uint64(len(c.shards))]
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.m) >= tokensPerShard {
		for k, e := range s.m {
			if !now.Before(e.until) {
				delete(s.m, k)
			}
		}
		for k := range s.m {
			if len(s.m) < tokensPerShard {
				break
			}
			delete(s.m, k)
		}
	}
	s.m[h] = cachedToken{token: token, p: p, until: until}
}
