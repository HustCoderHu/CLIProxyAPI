package helps

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha512"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/base64"
)

// COSY protocol constants mirrored from the official Qoder CLI 1.1.75.
// They live in one place so a version bump stays a single-file change.
const (
	qoderAlphabet  = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	qoderStdBase64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	qoderPad       = '$'

	cosyVersion    = "1.1.75"
	cosyClientType = "5"
	cosyUserAgent  = "undici"
	cosyClientIP   = "169.254.198.161"
	cosyLoginVer   = "v2"
)

// cosyDefaultUserType is used when the account does not expose a user type.
const cosyDefaultUserType = "personal_professional_trial"

// cosyPublicKeyPEM is the RSA public key the official client uses to wrap the
// per-session AES key. Rotating it invalidates every cached CosySession.
const cosyPublicKeyPEM = `-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----`

// Test seams. Production always uses the defaults; tests inject fixed values
// so every deterministic intermediate can be compared byte for byte against
// the reference implementation. Do not use t.Parallel in tests that touch them.
var (
	cosyNowFunc = time.Now
	// cosyRandomReader feeds the AES session key and the RSA padding.
	cosyRandomReader io.Reader = rand.Reader
	// cosyTempKeyFunc generates the 16 hex characters used as AES-128 key and IV.
	cosyTempKeyFunc = newCosyTempKey
)

// qoderStdToCustom maps standard base64 bytes onto the custom alphabet.
// Entries for bytes outside the 64-character set stay 0; '=' maps to qoderPad.
var qoderStdToCustom = func() (m [256]byte) {
	for i := 0; i < len(qoderStdBase64); i++ {
		m[qoderStdBase64[i]] = qoderAlphabet[i]
	}
	m['='] = qoderPad
	return m
}()

// qoderCustomToStd maps the custom alphabet back onto standard base64.
// Entries for characters outside the alphabet stay 0.
var qoderCustomToStd = func() (m [256]byte) {
	for i := 0; i < len(qoderAlphabet); i++ {
		m[qoderAlphabet[i]] = qoderStdBase64[i]
	}
	m[byte(qoderPad)] = '='
	return m
}()

// QoderEncode encodes plain into the Qoder custom Base64 variant used for
// outbound request bodies.
//
// The rotation is applied to STANDARD base64 output, never before encoding:
// with n = len(std) and a = n/3 the result is std[n-a:] + std[a:n-a] + std[:a].
// Only then are the 64 standard characters remapped onto qoderAlphabet, with
// '=' becoming qoderPad.
func QoderEncode(plain []byte) string {
	std := base64.StdEncoding.EncodeToString(plain)
	n := len(std)
	a := n / 3
	rotated := std[n-a:] + std[a:n-a] + std[:a]

	out := make([]byte, n)
	for i := 0; i < n; i++ {
		out[i] = qoderStdToCustom[rotated[i]]
	}
	return string(out)
}

// QoderDecode reverses QoderEncode.
func QoderDecode(encoded string) ([]byte, error) {
	n := len(encoded)
	std := make([]byte, n)
	for i := 0; i < n; i++ {
		mapped := qoderCustomToStd[encoded[i]]
		if mapped == 0 {
			return nil, fmt.Errorf("qoder decode: invalid character %q at offset %d", encoded[i], i)
		}
		std[i] = mapped
	}

	// The rotation is its own inverse: R = S[n-a:] + S[a:n-a] + S[:a] and
	// applying it again yields S because the three segments keep their lengths.
	a := n / 3
	restored := make([]byte, 0, n)
	restored = append(restored, std[n-a:]...)
	restored = append(restored, std[a:n-a]...)
	restored = append(restored, std[:a]...)

	plain, err := base64.StdEncoding.DecodeString(string(restored))
	if err != nil {
		return nil, fmt.Errorf("qoder decode: %w", err)
	}
	return plain, nil
}

// qoderJSONSortedCompact renders m as compact JSON with ascending keys and no
// whitespace. The upstream signs these exact bytes, so the output is assembled
// by hand instead of going through encoding/json.
//
// The escaper must match Python's json.dumps(..., ensure_ascii=False): '<',
// '>', '&' and U+2028/U+2029 are left raw, non-ASCII stays UTF-8, and only
// quotes, backslashes and control characters are escaped. ojson.AppendJSONString
// is not usable here because it escapes HTML characters by default and its
// switch, ojson.DisableEscapeHTML, is process-global and therefore unsafe to
// toggle from concurrent request handling.
func qoderJSONSortedCompact(m map[string]string) []byte {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]byte, 0, 64)
	out = append(out, '{')
	for i, k := range keys {
		if i > 0 {
			out = append(out, ',')
		}
		out = qoderAppendJSONString(out, k)
		out = append(out, ':')
		out = qoderAppendJSONString(out, m[k])
	}
	out = append(out, '}')
	return out
}

// qoderAppendJSONString appends s as a JSON string literal using the Python
// ensure_ascii=False escape set. Null values never reach it: callers pass the
// empty string instead.
func qoderAppendJSONString(dst []byte, s string) []byte {
	const hexDigits = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0x0f])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// qoderPKCS7Pad appends PKCS#7 padding. A full block is always added, including
// when len(plain) is already a multiple of the block size.
func qoderPKCS7Pad(plain []byte) []byte {
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	out := make([]byte, len(plain)+pad)
	copy(out, plain)
	for i := len(plain); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

// qoderAESCBCEncrypt encrypts plain with AES-128-CBC and PKCS#7 padding.
// The Qoder info blob uses the session temp key as both key and IV.
func qoderAESCBCEncrypt(plain, key, iv []byte) ([]byte, error) {
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("qoder cosy: aes key must be %d bytes, got %d", aes.BlockSize, len(key))
	}
	if len(iv) != aes.BlockSize {
		return nil, fmt.Errorf("qoder cosy: aes iv must be %d bytes, got %d", aes.BlockSize, len(iv))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("qoder cosy: new aes cipher: %w", err)
	}
	padded := qoderPKCS7Pad(plain)
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(out, padded)
	return out, nil
}

// qoderPublicKey lazily parses cosyPublicKeyPEM. The parsed key is immutable
// and safe for concurrent use.
func qoderPublicKey() (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(cosyPublicKeyPEM))
	if block == nil {
		return nil, errors.New("qoder cosy: public key PEM is malformed")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("qoder cosy: parse public key: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("qoder cosy: public key has type %T, want *rsa.PublicKey", parsed)
	}
	return key, nil
}

// qoderPKCS1v15Encrypt builds EM = 0x00 || 0x02 || ps || 0x00 || msg and
// encrypts it with the public exponent.
//
// crypto/rsa.EncryptPKCS1v15 is not used because it consumes one byte of the
// random stream with 50% probability (crypto/internal/randutil.MaybeReadByte),
// which would make the padding, and therefore the ciphertext, irreproducible
// under an injected stream. This is a public-key operation only, so the
// textbook form below is exact and side-channel free.
func qoderPKCS1v15Encrypt(key *rsa.PublicKey, msg, ps []byte) ([]byte, error) {
	if key == nil || key.N == nil {
		return nil, errors.New("qoder cosy: rsa public key is nil")
	}
	k := key.Size()
	if len(msg) > k-11 {
		return nil, fmt.Errorf("qoder cosy: message of %d bytes is too long for a %d byte RSA block", len(msg), k)
	}
	if len(ps) != k-3-len(msg) {
		return nil, fmt.Errorf("qoder cosy: padding must be %d bytes, got %d", k-3-len(msg), len(ps))
	}

	em := make([]byte, k)
	em[1] = 2
	copy(em[2:], ps)
	em[k-1-len(msg)] = 0
	copy(em[k-len(msg):], msg)

	m := new(big.Int).SetBytes(em)
	c := new(big.Int).Exp(m, big.NewInt(int64(key.E)), key.N)
	out := make([]byte, k)
	c.FillBytes(out)
	return out, nil
}

// qoderRSAPadding reads len bytes of nonzero pseudo-random padding, mirroring
// the reference implementation which discards zero bytes.
func qoderRSAPadding(random io.Reader, length int) ([]byte, error) {
	if length <= 0 {
		return nil, nil
	}
	ps := make([]byte, 0, length)
	buf := make([]byte, length)
	for len(ps) < length {
		need := length - len(ps)
		if _, err := io.ReadFull(random, buf[:need]); err != nil {
			return nil, fmt.Errorf("qoder cosy: read rsa padding: %w", err)
		}
		for _, b := range buf[:need] {
			if b != 0 {
				ps = append(ps, b)
			}
		}
	}
	return ps, nil
}

// qoderWrapTempKey wraps tempKey with the Qoder public key and returns the
// standard-base64 COSY key (not urlsafe, not the custom alphabet).
func qoderWrapTempKey(key *rsa.PublicKey, tempKey string, random io.Reader) (string, error) {
	ps, err := qoderRSAPadding(random, key.Size()-3-len(tempKey))
	if err != nil {
		return "", err
	}
	ciphertext, err := qoderPKCS1v15Encrypt(key, []byte(tempKey), ps)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ciphertext), nil
}

// newCosyTempKey returns a 16-character hexadecimal key, mirroring the
// reference implementation's uuid.uuid4().hex[:16] (16 hex characters, 64 bits
// of entropy). The value is only ever used as the AES-128 key/IV and as the RSA
// plaintext, so it never needs to survive a text transport; the restricted
// alphabet exists solely to match the official client.
func newCosyTempKey() (string, error) {
	var raw [16]byte
	if _, err := io.ReadFull(cosyRandomReader, raw[:]); err != nil {
		return "", fmt.Errorf("qoder cosy: read temp key: %w", err)
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, len(raw))
	for i := range raw {
		out[i] = hexDigits[raw[i]&0x0f]
	}
	return string(out), nil
}

// qoderFingerprintUID mirrors the reference fallback: an empty uid still
// produces a stable fingerprint, salted with "anonymous".
func qoderFingerprintUID(uid string) string {
	if uid == "" {
		return "anonymous"
	}
	return uid
}

// qoderDeriveID derives a hyphenated UUID-shaped identifier from uid and salt.
// Character 12 is forced to '4' and character 16 to 'a' so the result is shaped
// like the UUIDv4 the official client reports as a machine id.
func qoderDeriveID(uid, salt string) string {
	sum := md5.Sum([]byte(salt + ":" + qoderFingerprintUID(uid)))
	h := hex.EncodeToString(sum[:])
	h = h[:12] + "4" + h[13:16] + "a" + h[17:]
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// qoderMachineID returns the stable UUID-shaped machine id for uid.
func qoderMachineID(uid string) string { return qoderDeriveID(uid, "machine") }

// qoderMachineType returns the 18-character hyphen-free machine type for uid.
func qoderMachineType(uid string) string {
	return strings.ReplaceAll(qoderDeriveID(uid, "machinetype"), "-", "")[:18]
}

// qoderMachineToken returns the 43-character urlsafe-base64 machine token.
func qoderMachineToken(uid string) string {
	sum := sha512.Sum512([]byte("machinetoken:" + qoderFingerprintUID(uid)))
	token := base64.URLEncoding.EncodeToString(sum[:])
	return strings.TrimRight(token, "=")[:43]
}

// qoderMachineHostname returns the stable DESKTOP-XXXXXXX hostname for uid.
func qoderMachineHostname(uid string) string {
	sum := md5.Sum([]byte("hostname:" + qoderFingerprintUID(uid)))
	return "DESKTOP-" + strings.ToUpper(hex.EncodeToString(sum[:]))[:7]
}

// qoderMachineOS reports the platform identifier sent in cosy-machineos.
func qoderMachineOS() string {
	if runtime.GOOS == "windows" {
		return "x86_64_win32"
	}
	return "x86_64_linux"
}

// qoderSignPath extracts the signed path from rawURL: query string and fragment
// are excluded and a leading "/algo" gateway prefix is stripped.
func qoderSignPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Path == "" {
		return "/"
	}
	path := parsed.Path
	if strings.HasPrefix(path, "/algo") {
		path = path[len("/algo"):]
	}
	if path == "" {
		return "/"
	}
	return path
}

// qoderMD5Hex returns the lowercase hex MD5 digest of data.
func qoderMD5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// qoderNewRequestID returns a random RFC 4122 version 4 UUID string.
func qoderNewRequestID() string {
	var b [16]byte
	if _, err := io.ReadFull(cosyRandomReader, b[:]); err != nil {
		// Fall back to the timestamp so a request is never left unsigned.
		now := cosyNowFunc().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(now >> (8 * i))
		}
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// CosySession holds the per-account COSY material: the stable device
// fingerprint derived from the uid plus a per-session AES key wrapped with the
// Qoder public key. Rebuild it whenever the access token changes.
type CosySession struct {
	uid             string
	accessToken     string
	tempKey         string
	cosyKey         string
	info            string
	machineID       string
	machineType     string
	machineToken    string
	machineHostname string
	machineOS       string
}

// NewCosySession creates a session with a freshly generated AES key.
func NewCosySession(uid, nickname, accessToken, refreshToken, userType, orgID, orgName string) (*CosySession, error) {
	tempKey, err := cosyTempKeyFunc()
	if err != nil {
		return nil, err
	}
	return newCosySessionWithKey(uid, nickname, accessToken, refreshToken, userType, orgID, orgName, tempKey)
}

// newCosySessionWithKey creates a session with an injected AES key. It exists
// so tests can reproduce the reference implementation byte for byte.
func newCosySessionWithKey(uid, nickname, accessToken, refreshToken, userType, orgID, orgName, tempKey string) (*CosySession, error) {
	if accessToken == "" {
		return nil, errors.New("qoder cosy: empty access token")
	}
	if len(tempKey) != aes.BlockSize {
		return nil, fmt.Errorf("qoder cosy: temp key must be %d bytes, got %d", aes.BlockSize, len(tempKey))
	}
	key, err := qoderPublicKey()
	if err != nil {
		return nil, err
	}
	cosyKey, err := qoderWrapTempKey(key, tempKey, cosyRandomReader)
	if err != nil {
		return nil, err
	}

	// Identity has exactly nine keys. aid and uid both carry the uid, yx_uid is
	// always empty, and a missing user_type falls back to the trial tier.
	identity := map[string]string{
		"name":                 nickname,
		"aid":                  uid,
		"uid":                  uid,
		"yx_uid":               "",
		"organization_id":      orgID,
		"organization_name":    orgName,
		"user_type":            qoderUserType(userType),
		"security_oauth_token": accessToken,
		"refresh_token":        refreshToken,
	}
	encrypted, err := qoderAESCBCEncrypt(qoderJSONSortedCompact(identity), []byte(tempKey), []byte(tempKey))
	if err != nil {
		return nil, err
	}

	return &CosySession{
		uid:             uid,
		accessToken:     accessToken,
		tempKey:         tempKey,
		cosyKey:         cosyKey,
		info:            base64.StdEncoding.EncodeToString(encrypted),
		machineID:       qoderMachineID(uid),
		machineType:     qoderMachineType(uid),
		machineToken:    qoderMachineToken(uid),
		machineHostname: qoderMachineHostname(uid),
		machineOS:       qoderMachineOS(),
	}, nil
}

// qoderUserType applies the default user type when the account has none.
func qoderUserType(userType string) string {
	if userType == "" {
		return cosyDefaultUserType
	}
	return userType
}

// UID returns the account uid the session was built for.
func (s *CosySession) UID() string { return s.uid }

// Bearer builds the signed authorization header for one request. It returns
// the base64 payload, the Unix-seconds date string and the
// "Bearer COSY.<payload>.<signature>" value. The signature covers the ENCODED
// body, not the plaintext JSON.
func (s *CosySession) Bearer(encodedBody, rawURL string) (payloadB64, date, bearer string) {
	return s.bearerWithRequestID(encodedBody, rawURL, qoderNewRequestID())
}

// bearerWithRequestID is Bearer with an injected request id so the signed bytes
// are reproducible in tests.
func (s *CosySession) bearerWithRequestID(encodedBody, rawURL, requestID string) (payloadB64, date, bearer string) {
	// Payload has exactly five keys.
	payload := map[string]string{
		"cosyVersion": cosyVersion,
		"ideVersion":  "",
		"info":        s.info,
		"requestId":   requestID,
		"version":     "v1",
	}
	payloadB64 = base64.StdEncoding.EncodeToString(qoderJSONSortedCompact(payload))
	date = fmt.Sprintf("%d", cosyNowFunc().Unix())

	raw := payloadB64 + "\n" + s.cosyKey + "\n" + date + "\n" + encodedBody + "\n" + qoderSignPath(rawURL)
	bearer = "Bearer COSY." + payloadB64 + "." + qoderMD5Hex([]byte(raw))
	return payloadB64, date, bearer
}

// Headers returns the full COSY header set for one request.
//
// cosy-machinehostname and cache-control are only sent for server-sent-event
// calls; x-model-key and x-model-source are only sent when modelKey is set.
func (s *CosySession) Headers(encodedBody, rawURL, modelKey string, sse bool) map[string]string {
	_, date, bearer := s.Bearer(encodedBody, rawURL)
	accept := "application/json"
	if sse {
		accept = "text/event-stream"
	}
	headers := map[string]string{
		"cosy-data-policy":      "agree",
		"content-type":          "application/json",
		"cosy-business-product": "cli",
		"cosy-business-type":    "agent",
		"cosy-scene":            "assistant",
		"cosy-clienttype":       cosyClientType,
		"cosy-date":             date,
		"cosy-user":             s.uid,
		"cosy-key":              s.cosyKey,
		"accept":                accept,
		"authorization":         bearer,
		"accept-encoding":       "identity",
		"cosy-version":          cosyVersion,
		"cosy-machineid":        s.machineID,
		"cosy-machinetoken":     s.machineToken,
		"cosy-machinetype":      s.machineType,
		"cosy-clientip":         cosyClientIP,
		"login-version":         cosyLoginVer,
		"user-agent":            cosyUserAgent,
		"cosy-machineos":        s.machineOS,
	}
	if sse {
		headers["cosy-machinehostname"] = s.machineHostname
		headers["cache-control"] = "no-cache"
	}
	if modelKey != "" {
		headers["x-model-key"] = modelKey
		headers["x-model-source"] = "system"
	}
	return headers
}
