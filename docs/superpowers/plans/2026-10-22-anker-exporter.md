# Anker C1000 Exporter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go Prometheus exporter that shows live metrics of an Anker SOLIX C1000 (A1761) through the Anker cloud MQTT service.

**Architecture:** An API client logs in and fetches MQTT credentials. An MQTT session subscribes to the device topic and republishes the realtime trigger. A frame decoder turns each hex frame into named values. A collector exposes the latest values on `/metrics`. A live capture tool, built first, records real frames as golden test vectors.

**Tech Stack:** Go (stdlib `crypto/ecdh`, `crypto/aes`, `net/http`), `github.com/eclipse/paho.mqtt.golang`, `github.com/prometheus/client_golang`.

**Spec:** `docs/superpowers/specs/2026-10-22-anker-exporter-design.md`. The reference implementation is `thomluther/anker-solix-api`, directory `src/anker_solix_api`. Its files are `session.py`, `api.py`, `mqtt.py`, `mqttmap.py`, `mqttcmdmap.py`, `mqtttypes.py` and `apitypes.py`. Read them from `https://raw.githubusercontent.com/thomluther/anker-solix-api/main/src/anker_solix_api/<file>`.

## Global Constraints

- Language: Go. Single static binary. No Python at runtime.
- Read-only: the owner allows only read-only operations. The only MQTT publish is the realtime trigger (`0057`, topic `cmd/anker_power/A1761/{sn}/req`), and it needs the owner's explicit approval before the first send.
- Credentials for local tests live in `.envrc` (git-ignored, already created). Load them with `source .envrc`.
- API hosts: `https://ankerpower-api-eu.anker.com` (default) and `https://ankerpower-api.anker.com`.
- Login path `passport/login`. MQTT info path `app/devicemanage/get_user_mqtt_info`.
- MQTT: mutual TLS, port 8883, client id `{thing_name}_{5 random digits}`, subscribe `dt/anker_power/A1761/{sn}/#`.
- Headers: `content-type: application/json`, `model-type: DESKTOP`, `app-name: anker_power`, `os-type: android`, `country`, `timezone`. After login: `x-auth-token`, `gtoken` (md5 hex of `user_id`).
- Metric prefix `anker_c1000_`, label `device` (serial).
- Credentials come from env vars `ANKER_EMAIL`, `ANKER_PASSWORD`, `ANKER_REGION` (`eu` or `com`, default `eu`), `ANKER_COUNTRY` (default `GB`), `ANKER_DEVICE_SN` (optional). No secret appears in logs, errors, test files or git. Never write the password to a file.
- Git: repo-local `commit.gpgsign=false`. Conventional commits, no trailers or session URLs. Never push.

## Review Focus

- Wrong password: login error names the server code and contains no password. Test in Task 2.
- Token expires mid-run: the client logs in again and retries once. Test in Task 2.
- Frame with bad XOR checksum or truncated length: rejected, counter increments, no panic. Test in Task 4.
- Unknown field tag in a `0405` frame: skipped, counted, other fields still decode. Test in Task 4.
- Negative temperature (signed byte) and the Fahrenheit setting (`dd`): values report in Celsius. Test in Task 4.
- Broker disconnect or silent stop of frames: `anker_c1000_up` falls to 0 after twice the trigger interval and the trigger is resent. Test in Task 6 and 7.

---

### Task 1: Module scaffold and password encryption

**Files:**
- Create: `go.mod`, `.gitignore`, `internal/anker/crypto.go`
- Test: `internal/anker/crypto_test.go`

**Interfaces:**
- Produces: `func NewKeyExchange(serverPubHex string) (*KeyExchange, error)`; `func (k *KeyExchange) PublicKeyHex() string`; `func (k *KeyExchange) EncryptPassword(pw string) (string, error)`; `const ServerPublicKeyHex` (copy `_api_public_key_hex` from `session.py` verbatim).

- [ ] **Step 1:** Run `git init`, `git config core.fsmonitor false`, `git config commit.gpgsign false`, `go mod init github.com/lukaszraczylo/anker-exporter` (module path is the builder's call). `.gitignore` lists `capture/`, `*.pem`, `.env`.
- [ ] **Step 2: Write the failing test** `TestEncryptPassword_RoundTrip`. It builds a test server P-256 key pair and calls `NewKeyExchange` with that server public key. It decrypts the output on the server side (ECDH with the client public key, AES-256-CBC, key = shared secret, IV = first 16 bytes, PKCS7) and asserts it equals the input. Add `TestNewKeyExchange_RejectsBadKey` for a malformed hex string.
- [ ] **Step 3:** Run `go test ./internal/anker/ -run Crypto` (fails, undefined).
- [ ] **Step 4:** Implement with `crypto/ecdh` (P256), `crypto/aes`, `crypto/cipher`. Output is standard base64. The shared secret is the raw ECDH x coordinate, as in the Python `exchange`.
- [ ] **Step 5:** Run the tests (pass). Commit `feat(anker): add login password encryption`.

### Task 2: Anker API client

**Files:**
- Create: `internal/anker/client.go`
- Test: `internal/anker/client_test.go`

**Interfaces:**
- Consumes: `KeyExchange` (Task 1).
- Produces: `type Client`; `func NewClient(baseURL, email, password, country string, httpc *http.Client) (*Client, error)`; `func (c *Client) Login(ctx) error`; `func (c *Client) MQTTInfo(ctx) (MQTTInfo, error)`; `func (c *Client) Devices(ctx) ([]Device, error)`; `type MQTTInfo struct{ Endpoint, ThingName, UserID, CertificateID, AppName, CertPEM, KeyPEM, RootCAPEM string }`; `type Device struct{ SN, PN string }`. Every call except `Login` retries once after `Login` when the response signals an expired token.

- [ ] **Step 1: Read** `session.py` `async_authenticate`/`request` (error codes, token expiry handling) and `api.py` `get_bind_devices` (path, device fields). Cite the lines you used in code comments only if the reason is not obvious.
- [ ] **Step 2: Write failing tests** with `httptest.Server`: `TestLogin_SendsExpectedBody` (fields `ab`, `client_secret_info.public_key`, `enc:0`, `email`, `password` encrypted, `time_zone`, `transaction`; headers from Global Constraints), `TestLogin_WrongPassword` (error contains the server code, not the password), `TestMQTTInfo_ParsesFields`, `TestRequest_ReloginOnExpiredToken` (first call expired, second succeeds, login called twice), `TestDevices_FiltersA1761`.
- [ ] **Step 3:** Implement. After login, store `auth_token` and `user_id`, compute `gtoken` with md5. Server base URL is injectable for tests.
- [ ] **Step 4:** Run `go test ./internal/anker/` (pass). Commit `feat(anker): add cloud API client`.

### Task 3: Live capture tool (golden vectors)

**Files:**
- Create: `cmd/capture/main.go`, `internal/mqttsession/session.go` (connect part only), `testdata/frames.jsonl`

**Interfaces:**
- Produces: `func Connect(ctx, info anker.MQTTInfo, handler func(topic string, payload []byte)) (*Session, error)`; `func (s *Session) Subscribe(topic string) error`; `func (s *Session) Publish(topic string, payload []byte) error`; `func (s *Session) Close()`.

- [ ] **Step 1:** Implement `Connect` with Paho: TLS config from the PEM fields in `MQTTInfo` (client certificate, key, root CA), broker `ssl://{Endpoint}:8883`, client id per Global Constraints, clean session, auto-reconnect on.
- [ ] **Step 2:** `cmd/capture` reads credentials from env, logs in, lists devices, connects, subscribes to `dt/anker_power/A1761/{sn}/#` and writes every raw message to `testdata/frames.jsonl` (fields `topic`, `payload`, `ts`) for 60 s. It must not print or store tokens or the password. It does not send the trigger yet.
- [ ] **Step 3:** Run it with the owner's credentials exported in the shell: `ANKER_EMAIL=… ANKER_PASSWORD=… go run ./cmd/capture`. Record how many messages arrive. Zero messages is expected without the trigger, so go on to Step 4.
- [ ] **Step 4 (needs explicit owner approval first, the owner allows read-only operations only):** Publishing the trigger is a write to the device topic, so ask the owner before it runs. Do nothing in this step until they say yes. Build the trigger frame (`0057`, fields `a2=1`, `a3=60`, timestamp `fe`) following `mqtt.py` `publish` (message envelope) and `mqttcmdmap.py` (`CMD_REALTIME_TRIGGER`, `CMD_COMMON`, `pattern_22`). Publish it to the `cmd/…/req` topic, rerun the capture and confirm `0405` frames arrive about every 3 to 5 s. If they do not, stop and report what the broker returned. Do not guess more variants.
- [ ] **Step 5:** Replace the serial in `testdata/frames.jsonl` with `TESTSN0001`. Check with `grep -c` that the real serial, email and token strings are absent. Commit `test(capture): add live frame vectors`.

### Task 4: Frame decoder

**Files:**
- Create: `internal/frame/frame.go`, `internal/frame/a1761.go`
- Test: `internal/frame/frame_test.go`

**Interfaces:**
- Produces: `func Decode(envelope []byte) (Message, error)`; `type Message struct{ Type string; Values map[string]float64; Strings map[string]string; Unknown []string }`; `var ErrChecksum, ErrLength error`; `func BuildTrigger(sn, userID, clientID string, timeoutSec int, now time.Time) ([]byte, error)` (moved here from Task 3 once decoding is proven).

- [ ] **Step 1: Write failing tests** from the captured vectors: `TestDecode_0405GoldenVectors` (every `0405` line in `testdata/frames.jsonl` decodes; assert `main_battery_soc` is within 0–100, power fields are non-negative, serial equals `TESTSN0001`), `TestDecode_BadChecksum` (flip one byte, expect `ErrChecksum`), `TestDecode_Truncated` (expect `ErrLength`), `TestDecode_UnknownTag` (inject tag `ee`, expect it in `Unknown` and other values intact), `TestDecode_NegativeTemperature` (hand-built frame with a signed byte below zero), `TestDecode_FahrenheitNormalised` (`dd=1`, temperature reported in Celsius, if the capture shows the device reports in Fahrenheit; otherwise assert it is left unchanged and note why).
- [ ] **Step 2:** Implement envelope parsing (JSON `payload`, then base64 `data`), header (`ff09`, little-endian length incl. checksum, pattern, message type, optional counter) and the TLV field decoder. Use the value-type table from `apitypes.py` `DeviceHexDataTypes` and `mqtttypes.py` `DeviceHexDataField`. Field names and factors come from `_A1761_0405` in `mqttmap.py`; copy the map into a Go table in `a1761.go`.
- [ ] **Step 3:** Run `go test ./internal/frame/` (pass). Add `TestBuildTrigger_MatchesCapturedCommand` only if Task 3 produced a known-good frame. Commit `feat(frame): decode A1761 telemetry frames`.

### Task 5: Session loop (reconnect and trigger)

**Files:**
- Modify: `internal/mqttsession/session.go`
- Create: `internal/mqttsession/runner.go`
- Test: `internal/mqttsession/runner_test.go`

**Interfaces:**
- Consumes: `anker.Client`, `frame.Decode`, `frame.BuildTrigger`.
- Produces: `type Runner`; `func NewRunner(api TokenSource, sn string, trigger time.Duration, onMessage func(frame.Message)) *Runner`; `func (r *Runner) Run(ctx) error` (blocks until `ctx` ends). `TokenSource` is an interface with `MQTTInfo(ctx)` so tests can fake it.

- [ ] **Step 1: Write failing tests** with a fake `Session` interface: `TestRunner_ResendsTriggerBeforeTimeout` (fake clock or short durations: trigger sent at start and again before the timeout), `TestRunner_ReconnectsAfterDrop` (fetches fresh `MQTTInfo` after a disconnect), `TestRunner_DropsBadFrames` (checksum error does not stop the loop).
- [ ] **Step 2:** Implement the loop. Resend the trigger when 80% of the timeout has passed. On disconnect, back off (1 s doubling to 60 s) and fetch new MQTT info.
- [ ] **Step 3:** Run tests (pass). Commit `feat(session): add reconnect and trigger loop`.

### Task 6: Collector and HTTP server

**Files:**
- Create: `internal/collector/collector.go`
- Test: `internal/collector/collector_test.go`

**Interfaces:**
- Consumes: `frame.Message`.
- Produces: `func New(device string, staleAfter time.Duration, now func() time.Time) *Collector`; `func (c *Collector) Update(m frame.Message)`; `func (c *Collector) CountBadFrame()`; implements `prometheus.Collector`.

- [ ] **Step 1: Write failing tests** with `testutil.CollectAndCompare`: `TestCollector_ExposesGauges` (battery %, AC output W, temperature), `TestCollector_UpGoesZeroWhenStale` (`anker_c1000_up` is 0 and value gauges are absent after `staleAfter`), `TestCollector_CountsBadFrames`.
- [ ] **Step 2:** Implement. One gauge per entry in the Task 4 field map, named `anker_c1000_<field>` with the unit in the name where the map gives one (`_watts`, `_percent`, `_celsius`, `_seconds`). Add `anker_c1000_last_message_age_seconds` and `anker_c1000_bad_frames_total`.
- [ ] **Step 3:** Run tests (pass). Commit `feat(collector): expose telemetry as gauges`.

### Task 7: Main, config, container and live check

**Files:**
- Create: `cmd/anker-exporter/main.go`, `internal/config/config.go`, `Dockerfile`, `README.md`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `func Load(getenv func(string) string) (Config, error)`. It errors on missing `ANKER_EMAIL` or `ANKER_PASSWORD`, rejects other regions than `eu`/`com`, and never includes the password in `Config.String()`.

- [ ] **Step 1: Write failing tests** `TestLoad_Defaults`, `TestLoad_MissingPassword`, `TestLoad_BadRegion`, `TestConfigString_RedactsPassword`.
- [ ] **Step 2:** Implement `Load` and `main.go`: log in, pick the device (`ANKER_DEVICE_SN` or the only A1761), start the runner, serve `/metrics` and `/healthz` on `:9876` (flag `-listen`). Shut down on SIGTERM.
- [ ] **Step 3:** Write a multi-stage `Dockerfile` (distroless static, non-root). `README.md` lists the env vars, the metric names and the privacy and terms-of-service risk from the spec.
- [ ] **Step 4: Live check.** Run `go run ./cmd/anker-exporter` with the credentials exported, then `curl -s localhost:9876/metrics | grep anker_c1000_`. Expected: `anker_c1000_up 1` and battery and power values that match the device display. Show the owner the output. Check the logs with `grep -i` for the password and token. Expected: no match.
- [ ] **Step 5:** Run `go vet ./...` and `go test ./...` (pass). Commit `feat: add exporter entrypoint and container image`.
