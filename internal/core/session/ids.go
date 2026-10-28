package session

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// Identifiers, in loop's two shapes: a session is a ULID, an entry an 8-hex
// id unique within its session.

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	ulidMu   sync.Mutex
	ulidLast int64
	ulidRand [10]byte
)

// NewULID returns a 26-character ULID: 48 bits of milliseconds, 80 random
// bits. Within one millisecond the random part increments, so ids made in a
// burst still sort in creation order — the property loop's ulid() has too.
func NewULID() string {
	ulidMu.Lock()
	defer ulidMu.Unlock()
	ms := time.Now().UnixMilli()
	if ms == ulidLast {
		for i := len(ulidRand) - 1; i >= 0; i-- {
			ulidRand[i]++
			if ulidRand[i] != 0 {
				break
			}
		}
	} else {
		ulidLast = ms
		_, _ = rand.Read(ulidRand[:])
	}
	var b strings.Builder
	b.Grow(26)
	for i := 9; i >= 0; i-- {
		b.WriteByte(crockford[(ms>>(uint(i)*5))&31])
	}
	// 80 random bits as 16 base32 digits.
	var acc uint64
	bits := 0
	for _, by := range ulidRand {
		acc = acc<<8 | uint64(by)
		bits += 8
		for bits >= 5 {
			bits -= 5
			b.WriteByte(crockford[(acc>>uint(bits))&31])
		}
	}
	return b.String()
}

// newEntryID is loop's generateEntryId: 8 hex characters, retried against
// the ids the session already holds.
func newEntryID(taken func(string) bool) string {
	for i := 0; i < 100; i++ {
		var b [4]byte
		_, _ = rand.Read(b[:])
		id := hex.EncodeToString(b[:])
		if !taken(id) {
			return id
		}
	}
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
