# COSY golden vectors (generated from the Python reference)

Source: one-off read-only run of `E:/docs/github_repo/qoder2api-hub/qoder_sign.py`
and `qoder_fingerprint.py` on 2026-11-06. Reference code is NOT reused; only the
byte-exact outputs are recorded here so the Go port can be checked.

Reproduce with the same inputs; do not regenerate from guesswork.

---

## 1. Qoder custom Base64 (`qoder_encode`)

Alphabet: `_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!`
Pad char: `$`

Forward: `std = stdBase64(plain)`; `a = len(std)/3`;
`R = std[n-a:] + std[a:n-a] + std[:a]`; then map the 64 std chars + `=` to the
custom alphabet + `$`.

| input | len(plain) | expected encoded |
|---|---|---|
| `{}` | 2 | `$kwm` |
| `x` | 1 | `$_$m` |
| `xx` | 2 | `$Z&m` |
| `xxx` | 3 | `IZim` |
| `xxxx` | 4 | `$$iIm_mZ` |
| `xxxxx` | 5 | `&$iImZmZ` |

Boundary case (three-segment rotation over a long input):

- plain = `{"k":"` + 986 x `a` + `"}`, len 994
- encoded length 1328
- md5(encoded ASCII) = `7c1ff81fe2bb6518ce4c6dbbd27041dc`

Round trip: `qoder_decode(qoder_encode(p)) == p` for every vector above.
Decode is the inverse rotation: `S = R[2] + R[1] + R[0]` after alphabet mapping.

---

## 2. Sorted compact JSON (`json_sorted_compact`)

Keys sorted ascending, no whitespace, `null` rendered as `""`,
non-ASCII kept as UTF-8 (`ensure_ascii=False`).

Fixture identity (9 keys, all string values):

```json
{"aid":"uid-fixture-001","name":"alice","organization_id":"1234567890123456789","organization_name":"Acme","refresh_token":"drt-fixture-refresh-token","security_oauth_token":"drt-fixture-access-token","uid":"uid-fixture-001","user_type":"personal_professional_trial","yx_uid":""}
```

- byte length 279
- md5 = `be5d65f7782915f1113e6c2e3fed4173`

Minimal ordering check: `{"b":1,"a":"x"}` -> `{"a":"x","b":1}`.

---

## 3. AES-128-CBC (key == iv, PKCS#7)

tempKey is 16 ASCII bytes. Vectors use tempKey = `0123456789abcdef`.

| plaintext | len | expected std-base64 of ciphertext |
|---|---|---|
| `x` | 1 | `PF6O4udt26t7BXgQILH63w==` |
| `0123456789abcdef` | 16 | `C5sV2ktEoPUVHc/EwB811b8xuRnjiS3cO1khLWp7EeY=` |
| `0123456789abcdef0123456789abcdef` | 32 | `C5sV2ktEoPUVHc/EwB811bjIG1R3kPqBf9BFyYGipvBlc8jfNlPyIdGwf8zPYJ4v` |

The identity fixture from section 2 encrypts to `info` (std base64, length 384):
md5(info ASCII) = `8a64429617c05785acc412f9b4b4e8c2`.

Note both len%16==0 vectors must append a full 16-byte padding block.

---

## 4. Payload and signature

`info` = section 3 identity output (std base64).
Fixed requestId = `11111111-2222-4333-8444-555555555555`.

Sorted compact payload:

```json
{"cosyVersion":"1.1.75","ideVersion":"","info":"<info>","requestId":"11111111-2222-4333-8444-555555555555","version":"v1"}
```

- md5(payloadB64 ASCII) = `4a2579f66999386a426f588f4a809071`
- payloadB64 begins with `eyJjb3N5VmVyc2lvbiI6IjEuMS43NSIsImlkZVZlcnNpb24iOiIiLCJpbmZvIjoiK0tzdWZ6` and ends with `IiwicmVxdWVzdElkIjoiMTExMTExMTEtMjIyMi00MzMzLTg0NDQtNTU1NTU1NTU1NTU1IiwidmVyc2lvbiI6InYxIn0=`

Signature fixture:
- cosyKey (fixture, NOT a real RSA output) = `RkFLRUNPU1lLRVlGSVhUVVJF`
- date = `1762345678`
- encoded body = `$kwm`
- path = `/api/v2/chat/completions` (after stripping the leading `/algo`)
- raw = payloadB64 + `\n` + cosyKey + `\n` + date + `\n` + body + `\n` + path
- md5(raw) = `38cf23d4db6117fc09a64d5edd1a01b8`
- bearer = `Bearer COSY.` + payloadB64 + `.` + that md5

**Correction (2026-11-06)**: `/api/v2/chat/completions` used above is NOT a real
upstream endpoint. It is kept only as a signature-shape fixture so the md5 stays
comparable. The real paths are (from `qoder_proxy.py:330-332`):

| purpose | raw path | signed path (after `/algo` strip) | md5(raw) |
|---|---|---|---|
| chat inference | `/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1` | `/api/v2/service/pro/sse/agent_chat_generation` | `cb782693fbb3ea7f34eb8916f52aacd3` |
| model list | `/algo/api/v2/model/list?Encode=1` | `/api/v2/model/list` | `b6d0267867eda4e939b10c73445d4ca8` |

Both md5 values use the same payloadB64 / cosyKey / date / body fixtures as above.

Path stripping examples (corrected):
- `https://gateway.qoder.com.cn/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1` -> `/api/v2/service/pro/sse/agent_chat_generation`
- `https://gateway.qoder.com.cn/algo/api/v2/model/list?Encode=1` -> `/api/v2/model/list`

Query string must not enter the signature.

---

## 5. Device fingerprint (UID = `uid-fixture-001`)

| value | expected |
|---|---|
| machineId | `496f8fce-aa1a-4955-ac49-8454c8df4509` |
| machineType | `6c62004422954892aa` |
| machineToken | `mdWfxiSke2bEszXUeLy3QAbdxtPELtxJHQ9RvupjfTc` |
| hostname | `DESKTOP-2F7B5E2` |

Derivation:
- `machineId`: md5(`machine:<uid>`), then force char 12 -> `4`, char 16 -> `a`,
  then regroup as 8-4-4-4-12.
- `machineType`: md5(`machinetype:<uid>`) with the same UUID shaping, then
  remove hyphens and take the first 18 chars.
- `machineToken`: urlsafe-base64(sha512(`machinetoken:<uid>`)), strip `=`,
  first 43 chars.
- `hostname`: `DESKTOP-` + upper(md5(`hostname:<uid>`)[:7]).

`cosy-machineos` defaults to `x86_64_win32` on Windows and `x86_64_linux`
elsewhere.

---

## 6. RSA PKCS#1 v1.5 (deterministic padding injection)

Public key (SPKI PEM, 1024-bit, e = 65537) is copied from the reference client:

```
-----BEGIN PUBLIC KEY-----
MIGfMA0GCSqGSIb3DQEBAQUAA4GNADCBiQKBgQDA8iMH5c02LilrsERw9t6Pv5Nc
4k6Pz1EaDicBMpdpxKduSZu5OANqUq8er4GM95omAGIOPOh+Nx0spthYA2BqGz+l
6HRkPJ7S236FZz73In/KVuLnwI8JJ2CbuJap8kvheCCZpmAWpb/cPx/3Vr/J6I17
XcW+ML9FoCI6AOvOzwIDAQAB
-----END PUBLIC KEY-----
```

Deterministic padding stream: byte i = `(i % 255) + 1` for i = 0..199 (no
zeros), truncated to `k - 3 - len(plain)` = 109 bytes for a 16-byte plaintext.

- plaintext (tempKey) = `0123456789abcdef`
- ciphertext length 128
- std-base64(cosyKey) =
  `Cj3wcD3HVy3YStP9asK+maU0xaeJ+0i+AJFvy6i+Q39VgnuJe0GnqJU0Mayi+XpFWCYzM7RTCBIfB18c9rKDD+PkJln5VHof/k7KJ5ymBAsCN+AfDFAUkDFH7MczPDfqEftng53b7KbSMYcNi1mkhTNNEQq8e5YtOwHN2vPviQE=`
- md5 of that base64 ASCII = `597635aa4e4619051b6c785c9ff6c377`
- md5 of the 109-byte padding block = `80ebee18fd1256d8831eb0924030cbbb`

---

## 7. Non-comparable values

RSA encryption with a real random source, and therefore `cosyKey` and the final
bearer in production, are NOT deterministic. Only the intermediates in sections
1-6 are golden.
