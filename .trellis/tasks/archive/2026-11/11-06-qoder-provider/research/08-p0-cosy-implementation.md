# P0 完成记录: COSY 编码与签名

日期: 2026-11-06
范围: `implement.md` 的 P0 阶段 (P0.1 - P0.9). P1 及以上未动.

---

## 一、交付文件

| 文件 | 行数(约) | 内容 |
|---|---|---|
| `internal/runtime/executor/helps/qoder_cosy.go` | 525 | 自定义 Base64、排序紧凑 JSON、AES-CBC、RSA-PKCS1v15、设备指纹、签名路径、`CosySession` |
| `internal/runtime/executor/helps/qoder_cosy_test.go` | 600 | 黄金向量 + 形状 + 接缝测试, 24 个 `TestQoderCosy*` |

未改动任何其他文件 (除 `implement.md` 勾选).

---

## 二、黄金向量全部通过

`go test ./internal/runtime/executor/helps/ -run TestQoderCosy` 全绿.

| # | 向量 | 期望值 | 状态 |
|---|---|---|---|
| 1 | `QoderEncode` 6 组 (`{}` / x / xx / xxx / xxxx / xxxxx) | `$kwm` / `$_$m` / `$Z&m` / `IZim` / `$$iIm_mZ` / `&$iImZmZ` | PASS |
| 2 | 994 字节输入编码后长度与 md5 | 1328 / `7c1ff81fe2bb6518ce4c6dbbd27041dc` | PASS |
| 3 | identity 排序紧凑 JSON | 279 字节 / `be5d65f7782915f1113e6c2e3fed4173` | PASS |
| 4 | AES-CBC 三组 (1 / 16 / 32 字节明文) | `PF6O4udt26t7BXgQILH63w==` 等三组 | PASS |
| 5 | identity -> info | 384 字节 / `8a64429617c05785acc412f9b4b4e8c2` | PASS |
| 6 | payloadB64 (固定 info + 固定 requestId) | `4a2579f66999386a426f588f4a809071` + 首尾串联 | PASS |
| 7 | 签名 raw md5 (cosyKey 固定 / date 1762345678 / body `$kwm` / path 剥 `/algo`) | `38cf23d4db6117fc09a64d5edd1a01b8` | PASS |
| 8 | 设备指纹 4 项 (uid `uid-fixture-001`) | machineId / machineType / machineToken / hostname 全部一致 | PASS |
| 9 | RSA 确定性填充 (tempKey `0123456789abcdef`) | cosyKey 完全一致, 密文 128 字节, cosyKey md5 `597635aa4e4619051b6c785c9ff6c377`, 填充块 md5 `80ebee18fd1256d8831eb0924030cbbb` | PASS |
| 10 | path 剥离 | 两条示例 URL + 无前缀 + 空 path + 裸 `/algo` | PASS |

所有期望值用 Python 参考实现 `qoder2api-hub/qoder_sign.py` + `qoder_fingerprint.py` 独立复算过一遍, 与 `research/07-cosy-golden-vectors.md` 逐字一致. 未从 Go 代码反向生成.

---

## 三、调研中发现的三处与任务描述不符 (需主 agent 知悉)

### 1. `ojson.AppendJSONString` 默认会转义 `<` `>` `&`

dispatch 前提 ("verified: ojson does NOT escape `<`, `>`, `&` by default") **不成立**.
实测 (go test 临时用例):

```
"a<b>c&d" -> "a<b>c&d"
"  "     -> "  "
```

`ojson.DisableEscapeHTML` 是包级全局变量, 在并发请求路径里翻转它会造成数据竞争.

**处置**: 不使用 `ojson.AppendJSONString`, 改为手写 `qoderAppendJSONString`, 精确复刻 Python
`json.dumps(..., ensure_ascii=False)` 的转义集: 只转 `"` `\` 与 <0x20 控制字符;
`<` `>` `&` U+2028 U+2029 与非 ASCII 原样输出. 这样签名覆盖的字节与参考实现逐字节相同.
违反的只是"必须调用 ojson"这条形式要求, 实际语义要求 (与 Python 输出逐字节一致) 得到满足;
若主 agent 坚持形式要求, 需要先给 ojson 加并发安全的按调用点开关.

### 2. `rsa.EncryptPKCS1v15` 无法用注入随机源复现密文

Go 的 `crypto/rsa.EncryptPKCS1v15` 内部先调 `randutil.MaybeReadByte(random)`,
**50% 概率**多读 1 字节, 之后才读填充串. 因此即使注入固定 reader, 密文也随机漂移,
黄金向量 (research/07 第 6 节) 无法稳定复现.

**处置**: 手写 `qoderPKCS1v15Encrypt` (纯公钥模幂, `em = 0x00 || 0x02 || ps || 0x00 || msg`),
填充串由 `qoderRSAPadding` 按参考实现语义读 (丢弃 0 字节). 公钥运算无侧信道问题,
且结果与 Python 参考实现逐字节一致 (已用 109 字节填充块 md5 + cosyKey md5 双重锁定).
`implement.md` 里写的 "`rsa.EncryptPKCS1v15` 封装" 因此改为等价自实现.

### 3. research/07 的公钥转录风险

第一版手抄 PEM 时把 `ML9` 写成 `ML8`, 导致指纹与填充位错, 密文完全不同而**长度与格式都正常**,
只有黄金向量能发现. 现已在 `qoder_cosy.go` 中修正为与参考实现一致的
`XcW+ML9FoCI6AOvOzwIDAQAB`. 后续任何常量升级都必须先跑 `TestQoderCosyRSADeterministicPadding`.

### 4. tempKey 字符集: 已对齐参考实现

首版 `newCosyTempKey` 把 16 字节随机数映射到 62 种可打印 ASCII, 理由写作 "survives any text transport".
该理由**不成立**: tempKey 只作 AES key/IV 与 RSA 明文, 从不经过文本传输.
参考实现是 `uuid.uuid4().hex[:16]`, 即 16 个 `[0-9a-f]` 字符 / 64 bit 熵.
已改为 `hexDigits[raw[i]&0x0f]`, 与官方客户端字符集一致 (不影响正确性, 服务端只按字节解包,
但消除了一处无必要的指纹特征).

---

## 四、`QoderDecode` 的一个实现简化

参考实现写 `S = R3 + R2 + R1`, 注释称作"逆向". 实际该三段旋转**自身即为逆变换**
(三段长度 `a` / `n-2a` / `a` 在旋转后位置互换, 再转一次即还原),
所以 Go 版直接复用同一旋转表达式, 不需要额外的逆序拼接. 已由随机 1KB + 11 个边界长度的
往返测试锁定 (`TestQoderCosyDecodeRoundTripRandom`).

---

## 五、对外 API (供 P2 executor 使用)

```go
func QoderEncode(plain []byte) string
func QoderDecode(encoded string) ([]byte, error)

func NewCosySession(uid, nickname, accessToken, refreshToken, userType, orgID, orgName string) (*CosySession, error)
func (s *CosySession) UID() string
func (s *CosySession) Bearer(encodedBody, rawURL string) (payloadB64, date, bearer string)
func (s *CosySession) Headers(encodedBody, rawURL, modelKey string, sse bool) map[string]string
```

包内供 P1/P2 复用的未导出件:
`qoderJSONSortedCompact` / `qoderAppendJSONString` / `qoderPKCS7Pad` / `qoderAESCBCEncrypt` /
`qoderPublicKey` / `qoderPKCS1v15Encrypt` / `qoderRSAPadding` / `qoderWrapTempKey` /
`qoderMachineID` / `qoderMachineType` / `qoderMachineToken` / `qoderMachineHostname` /
`qoderMachineOS` / `qoderDeriveID` / `qoderSignPath` / `qoderMD5Hex` / `qoderNewRequestID`.

测试缝 (未导出, P1/P2 的测试同样可用):
`cosyNowFunc` / `cosyRandomReader` / `cosyTempKeyFunc` /
`newCosySessionWithKey` / `(s *CosySession) bearerWithRequestID` / `qoderPatternReader`.

只有 `QoderEncode` / `QoderDecode` / `CosySession` / `NewCosySession` 是导出符号;
如果 P2 需要从 `executor` 包直接构造 body 编码, 只依赖 `QoderEncode` 即可.

---

## 六、与 design.md 的差异说明

| design.md 2.3 | 实际实现 | 原因 |
|---|---|---|
| 头集合固定, 顺序不敏感 | 用 `map[string]string` | Go 的 header map 本身无序; 上游不依赖顺序 |
| `cosy-machinehostname` 仅 sse | 一致, 且 `cache-control: no-cache` 也仅 sse | 与参考实现一致 |
| `accept` 仅 sse 时 `text/event-stream` | non-sse 时为 `application/json` | 参考实现 `headers(accept="text/event-stream")` 默认值; 非 sse 显式传 `application/json` |
| `newCosySessionWithKey(..., tempKey string)` | 一致 | — |
| RSA 用 `rsa.EncryptPKCS1v15` | 自实现等价运算 | 见第三节第 2 点 |

`cosy-machineos` 在非 Windows 下按 design.md 取 `x86_64_linux` (design.md 未提 darwin/arm64;
参考实现默认同上, `auto` 模式不在本次范围).

---

## 七、验证命令与结果

```bash
gofmt -l internal/runtime/executor/helps/qoder_cosy.go internal/runtime/executor/helps/qoder_cosy_test.go
# 输出为空

go vet ./internal/runtime/executor/helps/
# 无输出

go test ./internal/runtime/executor/helps/ -run TestQoderCosy
# ok  github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps  0.035s

go build -o test-output ./cmd/server && rm -f test-output
# BUILD_OK
```

未跑 `go test ./...` 全量 (P0 不触碰其他包, 且任务说明已知 `TestClaudeCloakedDateReminder*` 既有失败).

---

## 八、给 P1 的注意事项

1. `QoderEncode` 的入参必须是**最终序列化后的 body 字节**, 不是 `*ojson.Doc`.
   P1 `BuildQoderBodyDoc` 应先 `doc.Bytes()`, 再 `QoderEncode`, 最后交给 `CosySession.Headers`
   (签名覆盖编码后 body, 顺序不可调换).
2. `Headers` 内部自己调用 `Bearer` 生成 `cosy-date` 与 `authorization`, 调用方不需要再单独取 date.
3. 如果 P1/P2 需要固定 `requestId` (例如重试复用同一 requestId), 目前只有未导出的
   `bearerWithRequestID`; 需要在同包内使用, 或届时再导出包装. 按 design.md 5.1, 重试应生成新 requestId
   (`request_id` / `request_set_id` 是计费与限额计数), 因此暂不导出.
4. 并发安全: `CosySession` 构造后全部字段只读, 可被多 goroutine 共享;
   但测试缝 `cosyNowFunc` / `cosyRandomReader` / `cosyTempKeyFunc` 是包级变量,
   带这些缝的测试**不能** `t.Parallel()`.
