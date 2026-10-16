package helps

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sync"
	"time"
)

var (
	openCodeSessionPattern = regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	base62Alphabet         = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	sessionIDMutex   sync.Mutex
	lastTimestampMs  int64
	sessionIDCounter uint64

	// Session affinity cache: clientSignal -> canonicalSessionID
	openCodeSessionCache sync.Map
)

// GenerateOfficialOpenCodeSessionID implements OpenCode's exact Identifier.descending("session") algorithm.
// Official source: anomalyco/opencode packages/app/src/utils/id.ts & packages/opencode/src/id/id.ts
func GenerateOfficialOpenCodeSessionID() string {
	sessionIDMutex.Lock()
	nowMs := time.Now().UnixMilli()
	if nowMs != lastTimestampMs {
		lastTimestampMs = nowMs
		sessionIDCounter = 0
	}
	sessionIDCounter++
	counter := sessionIDCounter
	sessionIDMutex.Unlock()

	// now = BigInt(currentTimestamp) * 0x1000n + BigInt(counter)
	current := (uint64(nowMs) * 0x1000) + (counter & 0xFFF)
	// descending: ~current & 0xFFFFFFFFFFFF (48-bit bitwise NOT)
	inverted := ^current & 0xFFFFFFFFFFFF

	timeBytes := []byte{
		byte(inverted >> 40),
		byte(inverted >> 32),
		byte(inverted >> 24),
		byte(inverted >> 16),
		byte(inverted >> 8),
		byte(inverted),
	}

	randBytes := make([]byte, 14)
	_, _ = rand.Read(randBytes)
	for i := range randBytes {
		randBytes[i] = base62Alphabet[randBytes[i]%62]
	}

	return "ses_" + hex.EncodeToString(timeBytes) + string(randBytes)
}

// CanonicalOpenCodeSessionID returns a descending-timestamp encoded session ID,
// caching it by signal to keep session affinity across multi-turn requests.
func CanonicalOpenCodeSessionID(signal string) string {
	if openCodeSessionPattern.MatchString(signal) {
		return signal
	}
	if signal == "" {
		return GenerateOfficialOpenCodeSessionID()
	}
	if val, ok := openCodeSessionCache.Load(signal); ok {
		return val.(string)
	}
	newSession := GenerateOfficialOpenCodeSessionID()
	openCodeSessionCache.Store(signal, newSession)
	return newSession
}

// OpenCodeProjectID generates a bare 40-character lowercase hex string from project signal.
func OpenCodeProjectID(project string) string {
	if project == "" {
		project = "opencode-default-project"
	}
	sum := sha256.Sum256([]byte("opencode-project\x00" + project))
	return hex.EncodeToString(sum[:20])
}
