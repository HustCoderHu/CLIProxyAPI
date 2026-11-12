package helps

import (
	"encoding/hex"
	"math"
	"regexp"
	"testing"
	"time"
)

func TestGenerateOfficialOpenCodeSessionID(t *testing.T) {
	beforeMs := time.Now().UnixMilli()
	id := GenerateOfficialOpenCodeSessionID()
	afterMs := time.Now().UnixMilli()

	if len(id) != 30 {
		t.Fatalf("expected session ID length 30, got %d (id=%s)", len(id), id)
	}
	if !openCodeSessionPattern.MatchString(id) {
		t.Fatalf("session ID format mismatch: %s", id)
	}

	// Verify that the time component in the session ID decodes to a valid current timestamp
	timeHex := id[4:16]
	timeBytes, err := hex.DecodeString(timeHex)
	if err != nil {
		t.Fatalf("failed to decode time hex: %v", err)
	}
	var inverted uint64
	for _, b := range timeBytes {
		inverted = (inverted << 8) | uint64(b)
	}

	current := ^inverted & 0xFFFFFFFFFFFF
	decodedTimestampMs := int64(current / 0x1000)

	// Since 48 bits holds 36 bits of millisecond timestamp (2^36 ms ≈ 2.18 years),
	// the decoded timestamp represents current timestamp modulo 2^36.
	const timeMask36 = (int64(1) << 36)
	expectedBefore := beforeMs % timeMask36
	expectedAfter := afterMs % timeMask36

	if decodedTimestampMs < expectedBefore-1000 || decodedTimestampMs > expectedAfter+1000 {
		t.Fatalf("decoded timestamp %d out of expected range [%d, %d] (diff=%dms)",
			decodedTimestampMs, expectedBefore, expectedAfter, int64(math.Abs(float64(decodedTimestampMs-expectedBefore))))
	}
}

func TestCanonicalOpenCodeSessionID_Affinity(t *testing.T) {
	signal1 := "claude-session-signal-alpha"
	id1 := CanonicalOpenCodeSessionID(signal1)
	id2 := CanonicalOpenCodeSessionID(signal1)

	if id1 != id2 {
		t.Fatalf("expected session affinity for same signal, got %s != %s", id1, id2)
	}

	signal2 := "claude-session-signal-beta"
	id3 := CanonicalOpenCodeSessionID(signal2)
	if id1 == id3 {
		t.Fatalf("expected different session IDs for different signals, got %s == %s", id1, id3)
	}

	// Valid incoming session ID should be preserved
	validSession := "ses_0123456789ababcdef12345678"
	preserved := CanonicalOpenCodeSessionID(validSession)
	if preserved != validSession {
		t.Fatalf("expected valid session to be preserved, got %s", preserved)
	}
}

func TestOpenCodeProjectID(t *testing.T) {
	hexPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)

	p1 := OpenCodeProjectID("")
	if len(p1) != 40 || !hexPattern.MatchString(p1) {
		t.Fatalf("invalid project ID for empty string: %s", p1)
	}

	p2 := OpenCodeProjectID("my-project")
	if len(p2) != 40 || !hexPattern.MatchString(p2) {
		t.Fatalf("invalid project ID for 'my-project': %s", p2)
	}

	p3 := OpenCodeProjectID("my-project")
	if p2 != p3 {
		t.Fatalf("expected deterministic project ID: %s != %s", p2, p3)
	}
}
