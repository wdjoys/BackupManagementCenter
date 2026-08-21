package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// NewUUIDv7 generates a UUIDv7 (time-ordered) as a hex string.
// Format: tttttttt-tttt-7rrr-vrrr-rrrrrrrrrrrr
// 48 bits timestamp (milliseconds since epoch), 74 bits random.
func NewUUIDv7() string {
	b := make([]byte, 16)
	now := time.Now().UnixMilli()
	// 48-bit timestamp
	b[0] = byte(now >> 40)
	b[1] = byte(now >> 32)
	b[2] = byte(now >> 24)
	b[3] = byte(now >> 16)
	b[4] = byte(now >> 8)
	b[5] = byte(now)
	// 4-bit version (7)
	b[6] = (b[6] & 0x0f) | 0x70
	// 2-bit variant (10)
	b[8] = (b[8] & 0x3f) | 0x80
	// Fill remaining with random
	if _, err := rand.Read(b[6:8]); err != nil {
		panic(fmt.Sprintf("uuid: crypto/rand failed: %v", err))
	}
	if _, err := rand.Read(b[8:16]); err != nil {
		panic(fmt.Sprintf("uuid: crypto/rand failed: %v", err))
	}
	// Format as hex with dashes
	dst := make([]byte, 36)
	hex.Encode(dst[0:8], b[0:4])
	dst[8] = '-'
	hex.Encode(dst[9:13], b[4:6])
	dst[13] = '-'
	hex.Encode(dst[14:18], b[6:8])
	dst[18] = '-'
	hex.Encode(dst[19:23], b[8:10])
	dst[23] = '-'
	hex.Encode(dst[24:36], b[10:16])
	return string(dst)
}