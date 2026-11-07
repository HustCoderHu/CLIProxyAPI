package helps

import (
	"bytes"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
)

// Golden vectors below are copied verbatim from
// .trellis/tasks/11-06-qoder-provider/research/07-cosy-golden-vectors.md, which
// was generated from the Python reference implementation. They must not be
// regenerated from the Go code, or the test stops being a cross-check.

// qoderPatternReader emits the reference deterministic padding stream:
// byte i = (i % 255) + 1, repeating per Read call from the current offset.
type qoderPatternReader struct{ offset int }

func (r *qoderPatternReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte((r.offset+i)%255) + 1
	}
	r.offset += len(p)
	return len(p), nil
}

// withCosySeams installs deterministic seams for the duration of fn.
func withCosySeams(t *testing.T, now time.Time, reader func() *qoderPatternReader) {
	t.Helper()
	prevNow := cosyNowFunc
	prevReader := cosyRandomReader
	prevTempKey := cosyTempKeyFunc
	t.Cleanup(func() {
		cosyNowFunc = prevNow
		cosyRandomReader = prevReader
		cosyTempKeyFunc = prevTempKey
	})
	cosyNowFunc = func() time.Time { return now }
	if reader != nil {
		cosyRandomReader = reader()
	}
}

func TestQoderCosyEncodeVectors(t *testing.T) {
	cases := []struct {
		name  string
		plain []byte
		want  string
	}{
		{"empty object", []byte("{}"), "$kwm"},
		{"one byte", []byte("x"), "$_$m"},
		{"two bytes", []byte("xx"), "$Z&m"},
		{"three bytes", []byte("xxx"), "IZim"},
		{"four bytes", []byte("xxxx"), "$$iIm_mZ"},
		{"five bytes", []byte("xxxxx"), "&$iImZmZ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := QoderEncode(tc.plain); got != tc.want {
				t.Fatalf("QoderEncode(%q) = %q, want %q", tc.plain, got, tc.want)
			}
			decoded, err := QoderDecode(tc.want)
			if err != nil {
				t.Fatalf("QoderDecode(%q): %v", tc.want, err)
			}
			if !bytes.Equal(decoded, tc.plain) {
				t.Fatalf("QoderDecode(%q) = %q, want %q", tc.want, decoded, tc.plain)
			}
		})
	}
}

func TestQoderCosyEncodeLongRotation(t *testing.T) {
	// 994-byte input exercises the three-segment rotation across block
	// boundaries; only the md5 of the encoded output is a stable fixture.
	plain := append([]byte(`{"k":"`), bytes.Repeat([]byte("a"), 986)...)
	plain = append(plain, []byte(`"}`)...)
	if len(plain) != 994 {
		t.Fatalf("fixture length = %d, want 994", len(plain))
	}
	encoded := QoderEncode(plain)
	if len(encoded) != 1328 {
		t.Fatalf("encoded length = %d, want 1328", len(encoded))
	}
	if got := qoderMD5Hex([]byte(encoded)); got != "7c1ff81fe2bb6518ce4c6dbbd27041dc" {
		t.Fatalf("encoded md5 = %s, want 7c1ff81fe2bb6518ce4c6dbbd27041dc", got)
	}
	decoded, err := QoderDecode(encoded)
	if err != nil {
		t.Fatalf("QoderDecode: %v", err)
	}
	if !bytes.Equal(decoded, plain) {
		t.Fatal("QoderDecode did not round-trip the 994-byte input")
	}
}

func TestQoderCosyDecodeRoundTripRandom(t *testing.T) {
	plain := make([]byte, 1024)
	if _, err := rand.Read(plain); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	for _, n := range []int{0, 1, 2, 3, 4, 5, 15, 16, 17, 1023, 1024} {
		chunk := plain[:n]
		encoded := QoderEncode(chunk)
		decoded, err := QoderDecode(encoded)
		if err != nil {
			t.Fatalf("QoderDecode(len=%d): %v", n, err)
		}
		if !bytes.Equal(decoded, chunk) {
			t.Fatalf("round trip failed at length %d", n)
		}
	}
}

func TestQoderCosyDecodeRejectsInvalidCharacter(t *testing.T) {
	if _, err := QoderDecode("$kwm "); err == nil {
		t.Fatal("QoderDecode accepted a character outside the alphabet")
	}
	if _, err := QoderDecode(QoderEncode([]byte("x")) + "!"); err == nil {
		// '!' is part of the alphabet, so this is a length error, not a
		// character error; either way decoding must fail.
		t.Fatal("QoderDecode accepted a truncated/padded stream")
	}
}

func TestQoderCosySortedCompactIdentity(t *testing.T) {
	identity := map[string]string{
		"aid":                  "uid-fixture-001",
		"name":                 "alice",
		"organization_id":      "1234567890123456789",
		"organization_name":    "Acme",
		"refresh_token":        "drt-fixture-refresh-token",
		"security_oauth_token": "drt-fixture-access-token",
		"uid":                  "uid-fixture-001",
		"user_type":            "personal_professional_trial",
		"yx_uid":               "",
	}
	got := qoderJSONSortedCompact(identity)
	if len(got) != 279 {
		t.Fatalf("identity length = %d, want 279", len(got))
	}
	if sum := qoderMD5Hex(got); sum != "be5d65f7782915f1113e6c2e3fed4173" {
		t.Fatalf("identity md5 = %s, want be5d65f7782915f1113e6c2e3fed4173", sum)
	}
	want := `{"aid":"uid-fixture-001","name":"alice","organization_id":"1234567890123456789",` +
		`"organization_name":"Acme","refresh_token":"drt-fixture-refresh-token",` +
		`"security_oauth_token":"drt-fixture-access-token","uid":"uid-fixture-001",` +
		`"user_type":"personal_professional_trial","yx_uid":""}`
	if string(got) != want {
		t.Fatalf("identity =\n%s\nwant\n%s", got, want)
	}
}

func TestQoderCosySortedCompactOrdering(t *testing.T) {
	cases := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"ascending keys", map[string]string{"b": "1", "a": "x"}, `{"a":"x","b":"1"}`},
		{"empty map", map[string]string{}, `{}`},
		{"empty value stays empty string", map[string]string{"k": ""}, `{"k":""}`},
		{"utf8 is not escaped", map[string]string{"k": "中文"}, `{"k":"中文"}`},
		{"html chars are not escaped", map[string]string{"k": "a<b>c&d"}, `{"k":"a<b>c&d"}`},
		{"quote and backslash", map[string]string{"k": "q\"b\\s"}, `{"k":"q\"b\\s"}`},
		{"control chars", map[string]string{"k": "\x01\n\t"}, `{"k":"\u0001\n\t"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(qoderJSONSortedCompact(tc.in)); got != tc.want {
				t.Fatalf("qoderJSONSortedCompact = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestQoderCosyAESCBCVectors(t *testing.T) {
	key := []byte("0123456789abcdef")
	cases := []struct {
		name  string
		plain []byte
		want  string
	}{
		{"one byte pads a full block", []byte("x"), "PF6O4udt26t7BXgQILH63w=="},
		{"exactly one block adds a full pad block", []byte("0123456789abcdef"), "C5sV2ktEoPUVHc/EwB811b8xuRnjiS3cO1khLWp7EeY="},
		{"two blocks add a full pad block", []byte("0123456789abcdef0123456789abcdef"), "C5sV2ktEoPUVHc/EwB811bjIG1R3kPqBf9BFyYGipvBlc8jfNlPyIdGwf8zPYJ4v"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ciphertext, err := qoderAESCBCEncrypt(tc.plain, key, key)
			if err != nil {
				t.Fatalf("qoderAESCBCEncrypt: %v", err)
			}
			if got := base64.StdEncoding.EncodeToString(ciphertext); got != tc.want {
				t.Fatalf("ciphertext = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestQoderCosyAESCBCRejectsWrongKeySize(t *testing.T) {
	if _, err := qoderAESCBCEncrypt([]byte("x"), []byte("short"), []byte("0123456789abcdef")); err == nil {
		t.Fatal("qoderAESCBCEncrypt accepted a 5-byte key")
	}
	if _, err := qoderAESCBCEncrypt([]byte("x"), []byte("0123456789abcdef"), []byte("short")); err == nil {
		t.Fatal("qoderAESCBCEncrypt accepted a 5-byte iv")
	}
}

// qoderIdentityFixture returns the reference identity map and its plaintext.
func qoderIdentityFixture() map[string]string {
	return map[string]string{
		"aid":                  "uid-fixture-001",
		"name":                 "alice",
		"organization_id":      "1234567890123456789",
		"organization_name":    "Acme",
		"refresh_token":        "drt-fixture-refresh-token",
		"security_oauth_token": "drt-fixture-access-token",
		"uid":                  "uid-fixture-001",
		"user_type":            "personal_professional_trial",
		"yx_uid":               "",
	}
}

func TestQoderCosyIdentityInfoVector(t *testing.T) {
	key := []byte("0123456789abcdef")
	encrypted, err := qoderAESCBCEncrypt(qoderJSONSortedCompact(qoderIdentityFixture()), key, key)
	if err != nil {
		t.Fatalf("qoderAESCBCEncrypt: %v", err)
	}
	info := base64.StdEncoding.EncodeToString(encrypted)
	if len(info) != 384 {
		t.Fatalf("info length = %d, want 384", len(info))
	}
	if sum := qoderMD5Hex([]byte(info)); sum != "8a64429617c05785acc412f9b4b4e8c2" {
		t.Fatalf("info md5 = %s, want 8a64429617c05785acc412f9b4b4e8c2", sum)
	}
}

func TestQoderCosyPayloadVector(t *testing.T) {
	session := &CosySession{info: qoderInfoFixture(t)}
	payloadB64 := base64.StdEncoding.EncodeToString(qoderJSONSortedCompact(map[string]string{
		"cosyVersion": cosyVersion,
		"ideVersion":  "",
		"info":        session.info,
		"requestId":   "11111111-2222-4333-8444-555555555555",
		"version":     "v1",
	}))
	if sum := qoderMD5Hex([]byte(payloadB64)); sum != "4a2579f66999386a426f588f4a809071" {
		t.Fatalf("payloadB64 md5 = %s, want 4a2579f66999386a426f588f4a809071", sum)
	}
	const wantPrefix = "eyJjb3N5VmVyc2lvbiI6IjEuMS43NSIsImlkZVZlcnNpb24iOiIiLCJpbmZvIjoiK0tzdWZ6"
	if !strings.HasPrefix(payloadB64, wantPrefix) {
		t.Fatalf("payloadB64 prefix = %s, want %s", payloadB64[:len(wantPrefix)], wantPrefix)
	}
	const wantSuffix = "IiwicmVxdWVzdElkIjoiMTExMTExMTEtMjIyMi00MzMzLTg0NDQtNTU1NTU1NTU1NTU1IiwidmVyc2lvbiI6InYxIn0="
	if !strings.HasSuffix(payloadB64, wantSuffix) {
		t.Fatalf("payloadB64 suffix = %s, want %s", payloadB64[len(payloadB64)-len(wantSuffix):], wantSuffix)
	}
}

// qoderInfoFixture returns the reference info value, the standard-base64
// AES-CBC encryption of the identity fixture under tempKey 0123456789abcdef.
func qoderInfoFixture(t *testing.T) string {
	t.Helper()
	key := []byte("0123456789abcdef")
	encrypted, err := qoderAESCBCEncrypt(qoderJSONSortedCompact(qoderIdentityFixture()), key, key)
	if err != nil {
		t.Fatalf("qoderAESCBCEncrypt: %v", err)
	}
	return base64.StdEncoding.EncodeToString(encrypted)
}

func TestQoderCosySignatureVector(t *testing.T) {
	session := &CosySession{
		uid:     "uid-fixture-001",
		cosyKey: "RkFLRUNPU1lLRVlGSVhUVVJF",
		info:    qoderInfoFixture(t),
	}
	withCosySeams(t, time.Unix(1762345678, 0), nil)

	payloadB64, date, bearer := session.bearerWithRequestID(
		"$kwm",
		"https://gateway.qoder.com.cn/algo/api/v2/chat/completions?AgentId=agent_common",
		"11111111-2222-4333-8444-555555555555",
	)
	if date != "1762345678" {
		t.Fatalf("date = %s, want 1762345678", date)
	}
	if sum := qoderMD5Hex([]byte(payloadB64)); sum != "4a2579f66999386a426f588f4a809071" {
		t.Fatalf("payloadB64 md5 = %s, want 4a2579f66999386a426f588f4a809071", sum)
	}
	wantBearer := "Bearer COSY." + payloadB64 + ".38cf23d4db6117fc09a64d5edd1a01b8"
	if bearer != wantBearer {
		t.Fatalf("bearer = %s, want %s", bearer, wantBearer)
	}
}

// TestQoderCosySignatureVectorRealPaths pins the reference MD5 for the two real
// upstream paths. The legacy /api/v2/chat/completions fixture above is only a
// signature-shape check; these are the endpoints the executor actually calls, so
// a wrong path (which local mock tests cannot detect) fails here.
func TestQoderCosySignatureVectorRealPaths(t *testing.T) {
	session := &CosySession{
		uid:     "uid-fixture-001",
		cosyKey: "RkFLRUNPU1lLRVlGSVhUVVJF",
		info:    qoderInfoFixture(t),
	}
	withCosySeams(t, time.Unix(1762345678, 0), nil)

	cases := []struct {
		name string
		url  string
		// wantMD5 is the md5 of
		// payloadB64 + "\n" + cosyKey + "\n" + date + "\n" + body + "\n" + path
		// recorded from the Python reference (research/07-cosy-golden-vectors.md).
		wantMD5 string
	}{
		{
			name:    "chat inference",
			url:     "https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1",
			wantMD5: "cb782693fbb3ea7f34eb8916f52aacd3",
		},
		{
			name:    "model list",
			url:     "https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1",
			wantMD5: "b6d0267867eda4e939b10c73445d4ca8",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payloadB64, _, bearer := session.bearerWithRequestID("$kwm", tc.url, "11111111-2222-4333-8444-555555555555")
			if want := "Bearer COSY." + payloadB64 + "." + tc.wantMD5; bearer != want {
				t.Fatalf("bearer = %s, want %s", bearer, want)
			}
		})
	}
}

func TestQoderCosySignPath(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{
			"strips /algo and the query string",
			"https://gateway.qoder.com.cn/algo/api/v2/chat/completions?AgentId=agent_common",
			"/api/v2/chat/completions",
		},
		{
			"strips /algo and the encode flag",
			"https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1",
			"/api/v2/model/list",
		},
		{
			"keeps a path without the prefix",
			"https://api3.qoder.sh/api/v2/chat/completions",
			"/api/v2/chat/completions",
		},
		{
			"empty path falls back to a slash",
			"https://gateway.qoder.com.cn",
			"/",
		},
		{
			"the bare /algo prefix falls back to a slash",
			"https://gateway.qoder.com.cn/algo",
			"/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := qoderSignPath(tc.url); got != tc.want {
				t.Fatalf("qoderSignPath(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

func TestQoderCosyFingerprint(t *testing.T) {
	const uid = "uid-fixture-001"
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"machineId", qoderMachineID(uid), "496f8fce-aa1a-4955-ac49-8454c8df4509"},
		{"machineType", qoderMachineType(uid), "6c62004422954892aa"},
		{"machineToken", qoderMachineToken(uid), "mdWfxiSke2bEszXUeLy3QAbdxtPELtxJHQ9RvupjfTc"},
		{"hostname", qoderMachineHostname(uid), "DESKTOP-2F7B5E2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		})
	}
}

func TestQoderCosyFingerprintShapes(t *testing.T) {
	const uid = "uid-fixture-001"
	id := qoderMachineID(uid)
	if len(id) != 36 {
		t.Fatalf("machineId length = %d, want 36", len(id))
	}
	if id[14] != '4' || id[19] != 'a' {
		t.Fatalf("machineId = %q, want version nibble 4 and variant nibble a", id)
	}
	if len(qoderMachineType(uid)) != 18 {
		t.Fatalf("machineType length = %d, want 18", len(qoderMachineType(uid)))
	}
	if strings.Contains(qoderMachineType(uid), "-") {
		t.Fatalf("machineType = %q, want no hyphens", qoderMachineType(uid))
	}
	if len(qoderMachineToken(uid)) != 43 {
		t.Fatalf("machineToken length = %d, want 43", len(qoderMachineToken(uid)))
	}
	if strings.Contains(qoderMachineToken(uid), "=") {
		t.Fatalf("machineToken = %q, want no padding", qoderMachineToken(uid))
	}
	if !strings.HasPrefix(qoderMachineHostname(uid), "DESKTOP-") {
		t.Fatalf("hostname = %q, want the DESKTOP- prefix", qoderMachineHostname(uid))
	}
}

func TestQoderCosyFingerprintEmptyUID(t *testing.T) {
	// The reference salts with "anonymous" but keeps the uid itself empty.
	sum := sha512.Sum512([]byte("machinetoken:anonymous"))
	want := strings.TrimRight(base64.URLEncoding.EncodeToString(sum[:]), "=")[:43]
	if got := qoderMachineToken(""); got != want {
		t.Fatalf("machineToken for an empty uid = %q, want %q", got, want)
	}
	if qoderMachineID("") == qoderMachineID("uid-fixture-001") {
		t.Fatal("an empty uid produced the same machine id as a real uid")
	}
}

func TestQoderCosyRSADeterministicPadding(t *testing.T) {
	key, err := qoderPublicKey()
	if err != nil {
		t.Fatalf("qoderPublicKey: %v", err)
	}
	if key.Size() != 128 {
		t.Fatalf("public key size = %d bytes, want 128", key.Size())
	}
	if key.E != 65537 {
		t.Fatalf("public exponent = %d, want 65537", key.E)
	}

	cosyKey, err := qoderWrapTempKey(key, "0123456789abcdef", &qoderPatternReader{})
	if err != nil {
		t.Fatalf("qoderWrapTempKey: %v", err)
	}
	const want = "Cj3wcD3HVy3YStP9asK+maU0xaeJ+0i+AJFvy6i+Q39VgnuJe0GnqJU0Mayi+XpFWCYzM7RTCBIfB18c9rKDD+PkJln5VHof/k7KJ5ymBAsCN+AfDFAUkDFH7MczPDfqEftng53b7KbSMYcNi1mkhTNNEQq8e5YtOwHN2vPviQE="
	if cosyKey != want {
		t.Fatalf("cosyKey = %s, want %s", cosyKey, want)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(cosyKey)
	if err != nil {
		t.Fatalf("decode cosyKey: %v", err)
	}
	if len(ciphertext) != 128 {
		t.Fatalf("ciphertext length = %d, want 128", len(ciphertext))
	}
	if sum := qoderMD5Hex([]byte(cosyKey)); sum != "597635aa4e4619051b6c785c9ff6c377" {
		t.Fatalf("cosyKey md5 = %s, want 597635aa4e4619051b6c785c9ff6c377", sum)
	}
}

func TestQoderCosyRSAPaddingStream(t *testing.T) {
	// The reference discards zero bytes. Verify that behaviour explicitly.
	key, err := qoderPublicKey()
	if err != nil {
		t.Fatalf("qoderPublicKey: %v", err)
	}
	psLen := key.Size() - 3 - 16
	ps, err := qoderRSAPadding(&qoderPatternReader{}, psLen)
	if err != nil {
		t.Fatalf("qoderRSAPadding: %v", err)
	}
	if len(ps) != psLen {
		t.Fatalf("padding length = %d, want %d", len(ps), psLen)
	}
	for i, b := range ps {
		if b == 0 {
			t.Fatalf("padding byte %d is zero, want nonzero", i)
		}
	}
	if sum := qoderMD5Hex(ps); sum != "80ebee18fd1256d8831eb0924030cbbb" {
		t.Fatalf("padding md5 = %s, want 80ebee18fd1256d8831eb0924030cbbb", sum)
	}
}

func TestQoderCosyNewSessionRejectsEmptyToken(t *testing.T) {
	if _, err := NewCosySession("uid", "nick", "", "refresh", "", "", ""); err == nil {
		t.Fatal("NewCosySession accepted an empty access token")
	}
	if _, err := newCosySessionWithKey("uid", "nick", "", "refresh", "", "", "", "0123456789abcdef"); err == nil {
		t.Fatal("newCosySessionWithKey accepted an empty access token")
	}
}

func TestQoderCosyNewSessionRejectsBadTempKey(t *testing.T) {
	if _, err := newCosySessionWithKey("uid", "nick", "token", "", "", "", "", "short"); err == nil {
		t.Fatal("newCosySessionWithKey accepted a 5-byte temp key")
	}
}

func TestQoderCosyTempKeyIsHex(t *testing.T) {
	// The reference uses uuid.uuid4().hex[:16]: 16 lowercase hex characters.
	// The pattern reader yields 1..16, which maps through the low nibble to
	// 1..9 then a..f then 0, so the expected value is fully deterministic.
	withCosySeams(t, time.Unix(1762345678, 0), func() *qoderPatternReader { return &qoderPatternReader{} })
	key, err := newCosyTempKey()
	if err != nil {
		t.Fatalf("newCosyTempKey: %v", err)
	}
	if key != "123456789abcdef0" {
		t.Fatalf("temp key = %q, want 123456789abcdef0", key)
	}
	if len(key) != 16 {
		t.Fatalf("temp key length = %d, want 16", len(key))
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			t.Fatalf("temp key %q has a non-hex character at offset %d", key, i)
		}
	}
}

func TestQoderCosyTempKeyUsesRandomSource(t *testing.T) {
	// A different stream must produce a different key: the low nibble of each
	// random byte is what selects the character.
	prevReader := cosyRandomReader
	t.Cleanup(func() { cosyRandomReader = prevReader })
	cosyRandomReader = bytes.NewReader(bytes.Repeat([]byte{0xff}, 16))
	allF, err := newCosyTempKey()
	if err != nil {
		t.Fatalf("newCosyTempKey: %v", err)
	}
	if allF != "ffffffffffffffff" {
		t.Fatalf("temp key = %q, want ffffffffffffffff", allF)
	}
}

func TestQoderCosySessionIdentityDefaultUserType(t *testing.T) {
	withCosySeams(t, time.Unix(1762345678, 0), func() *qoderPatternReader { return &qoderPatternReader{} })
	session, err := newCosySessionWithKey("uid-fixture-001", "alice", "drt-fixture-access-token", "drt-fixture-refresh-token", "", "1234567890123456789", "Acme", "0123456789abcdef")
	if err != nil {
		t.Fatalf("newCosySessionWithKey: %v", err)
	}
	if session.info != qoderInfoFixture(t) {
		t.Fatal("session info does not match the reference identity fixture")
	}
	if session.machineID != "496f8fce-aa1a-4955-ac49-8454c8df4509" {
		t.Fatalf("session machineId = %q", session.machineID)
	}
	if session.UID() != "uid-fixture-001" {
		t.Fatalf("session uid = %q", session.UID())
	}
}

func TestQoderCosySessionBearerUsesEncodedBody(t *testing.T) {
	// The signature must cover the encoded body byte for byte: feeding the
	// plaintext instead must produce a different bearer.
	session := &CosySession{
		uid:     "uid-fixture-001",
		cosyKey: "RkFLRUNPU1lLRVlGSVhUVVJF",
		info:    qoderInfoFixture(t),
	}
	withCosySeams(t, time.Unix(1762345678, 0), nil)

	plain := `{"hello":"world"}`
	encoded := QoderEncode([]byte(plain))
	if encoded == plain {
		t.Fatal("QoderEncode returned the plaintext unchanged")
	}

	_, _, encodedBearer := session.bearerWithRequestID(encoded, "https://gateway.qoder.com.cn/algo/api/v2/chat/completions", "11111111-2222-4333-8444-555555555555")
	_, _, plainBearer := session.bearerWithRequestID(plain, "https://gateway.qoder.com.cn/algo/api/v2/chat/completions", "11111111-2222-4333-8444-555555555555")
	if encodedBearer == plainBearer {
		t.Fatal("bearer is identical for the encoded and plaintext body")
	}
}

func TestQoderCosyHeadersSSE(t *testing.T) {
	session := &CosySession{
		uid:             "uid-fixture-001",
		cosyKey:         "RkFLRUNPU1lLRVlGSVhUVVJF",
		info:            qoderInfoFixture(t),
		machineID:       "496f8fce-aa1a-4955-ac49-8454c8df4509",
		machineType:     "6c62004422954892aa",
		machineToken:    "mdWfxiSke2bEszXUeLy3QAbdxtPELtxJHQ9RvupjfTc",
		machineHostname: "DESKTOP-2F7B5E2",
		machineOS:       "x86_64_win32",
	}
	withCosySeams(t, time.Unix(1762345678, 0), nil)
	headers := session.Headers("$kwm", "https://gateway.qoder.com.cn/algo/api/v2/chat/completions", "", true)

	want := map[string]string{
		"cosy-data-policy":      "agree",
		"content-type":          "application/json",
		"cosy-business-product": "cli",
		"cosy-business-type":    "agent",
		"cosy-scene":            "assistant",
		"cosy-clienttype":       "5",
		"cosy-date":             "1762345678",
		"cosy-user":             "uid-fixture-001",
		"cosy-key":              "RkFLRUNPU1lLRVlGSVhUVVJF",
		"accept":                "text/event-stream",
		"accept-encoding":       "identity",
		"cosy-version":          "1.1.75",
		"cosy-machineid":        "496f8fce-aa1a-4955-ac49-8454c8df4509",
		"cosy-machinetoken":     "mdWfxiSke2bEszXUeLy3QAbdxtPELtxJHQ9RvupjfTc",
		"cosy-machinetype":      "6c62004422954892aa",
		"cosy-clientip":         "169.254.198.161",
		"login-version":         "v2",
		"user-agent":            "undici",
		"cosy-machineos":        "x86_64_win32",
		"cosy-machinehostname":  "DESKTOP-2F7B5E2",
		"cache-control":         "no-cache",
	}
	for k, v := range want {
		if got, ok := headers[k]; !ok {
			t.Fatalf("missing header %q", k)
		} else if got != v {
			t.Fatalf("header %q = %q, want %q", k, got, v)
		}
	}
	if _, ok := headers["x-model-key"]; ok {
		t.Fatal("x-model-key present without a model key")
	}
	if _, ok := headers["x-model-source"]; ok {
		t.Fatal("x-model-source present without a model key")
	}
	if wantLen := len(want) + 1; len(headers) != wantLen {
		t.Fatalf("header count = %d, want %d (authorization plus the listed keys)", len(headers), wantLen)
	}
	if _, ok := headers["authorization"]; !ok {
		t.Fatal("missing authorization header")
	}
}

func TestQoderCosyHeadersNonSSE(t *testing.T) {
	session := &CosySession{
		uid:       "uid-fixture-001",
		cosyKey:   "RkFLRUNPU1lLRVlGSVhUVVJF",
		info:      qoderInfoFixture(t),
		machineOS: "x86_64_win32",
	}
	withCosySeams(t, time.Unix(1762345678, 0), nil)
	headers := session.Headers("$kwm", "https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1", "", false)

	if headers["accept"] != "application/json" {
		t.Fatalf("accept = %q, want application/json", headers["accept"])
	}
	for _, k := range []string{"cosy-machinehostname", "cache-control"} {
		if _, ok := headers[k]; ok {
			t.Fatalf("header %q must not be sent for non-SSE requests", k)
		}
	}
	if headers["cosy-machineos"] != "x86_64_win32" {
		t.Fatalf("cosy-machineos = %q, want x86_64_win32", headers["cosy-machineos"])
	}
}

func TestQoderCosyHeadersModelKey(t *testing.T) {
	session := &CosySession{
		uid:       "uid-fixture-001",
		cosyKey:   "RkFLRUNPU1lLRVlGSVhUVVJF",
		info:      qoderInfoFixture(t),
		machineOS: "x86_64_linux",
	}
	withCosySeams(t, time.Unix(1762345678, 0), nil)
	headers := session.Headers("$kwm", "https://api3.qoder.sh/algo/api/v2/chat/completions", "qmodel_latest", true)
	if headers["x-model-key"] != "qmodel_latest" {
		t.Fatalf("x-model-key = %q, want qmodel_latest", headers["x-model-key"])
	}
	if headers["x-model-source"] != "system" {
		t.Fatalf("x-model-source = %q, want system", headers["x-model-source"])
	}
}

func TestQoderCosyRequestIDShape(t *testing.T) {
	first := qoderNewRequestID()
	second := qoderNewRequestID()
	if first == second {
		t.Fatalf("two request ids are identical: %s", first)
	}
	if len(first) != 36 {
		t.Fatalf("request id length = %d, want 36", len(first))
	}
	if first[14] != '4' {
		t.Fatalf("request id = %q, want version nibble 4", first)
	}
	if first[8] != '-' || first[13] != '-' || first[18] != '-' || first[23] != '-' {
		t.Fatalf("request id = %q, want UUID hyphen layout", first)
	}
	if _, err := hex.DecodeString(strings.ReplaceAll(first, "-", "")); err != nil {
		t.Fatalf("request id = %q is not hex: %v", first, err)
	}
}
