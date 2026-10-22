# Anker SOLIX C1000 Prometheus exporter: design

## Intent

Show live metrics of one Anker SOLIX C1000 (model A1761) in Grafana. The owner has not decided which panels to build. They want to see what the device exports first, then decide.

The device is on the home LAN and registered to the owner's Anker account. The owner does not use the Anker phone app.

## Why the cloud

The device has no local LAN API. The official Anker Modbus integration does not list the C1000. A Bluetooth spike on a Mac found no advertising device in two scans, so Bluetooth is dropped. The data path is the Anker cloud: log in, fetch MQTT credentials, subscribe to the device topic over mutual TLS.

## Goals

- One static Go binary that exposes `/metrics` for Prometheus.
- Every decoded telemetry field becomes a gauge, so the owner can see what exists.
- Runs unattended: re-logs in before token expiry, reconnects MQTT, resends the realtime trigger.
- Golden-vector tests built from captured real frames.

## Non-goals

- No device control. The only message the exporter publishes is the realtime trigger.
- No Home Assistant, no Python, no support for other Anker models in the first version.
- No dashboard in the first version. The Grafana panels follow once the metrics are visible.

## Anti-goals

- Do not log out or break the owner's Anker session more than necessary.
- Do not write credentials, tokens or private keys to logs or to disk in clear text.
- Do not switch AC or DC outputs, even by accident.

## Protocol (verified from `thomluther/anker-solix-api`, src/anker_solix_api)

The reference is a Python library. The Go port must reproduce this behaviour.

1. **Login.** `POST {api}/passport/login` with JSON: `ab` (country code), `client_secret_info.public_key` (client P-256 public key, uncompressed hex), `enc: 0`, `email`, `password`, `time_zone` (UTC offset in ms), `transaction` (Unix ms as string).
   - `password` is AES-256-CBC with PKCS7 padding, base64 encoded. The key is the ECDH P-256 shared secret between a fresh client key and Anker's fixed server public key. The IV is the first 16 bytes of that secret.
   - The server public key is in `session.py` as `_api_public_key_hex`. Copy it from there at build time.
   - API hosts: `https://ankerpower-api-eu.anker.com` and `https://ankerpower-api.anker.com`.
   - Headers: `content-type: application/json`, `model-type: DESKTOP`, `app-name: anker_power`, `os-type: android`, `country`, `timezone`. After login also `x-auth-token` and `gtoken` (md5 hex of `user_id`).
   - The response `data` holds `auth_token`, `user_id`, `token_expires_at`.
2. **MQTT info.** `POST {api}/app/devicemanage/get_user_mqtt_info`. `data` holds `endpoint_addr`, `thing_name`, `certificate_pem`, `private_key`, `aws_root_ca1_pem`, `user_id`, `certificate_id`, `app_name`.
3. **Device list.** The library lists devices through `get_bind_devices` in `api.py`. The builder must read it to find the A1761 serial and its endpoint. The owner may also pass the serial by env var.
4. **MQTT connection.** TLS with the client certificate and key, the AWS root CA, port 8883, `clean_session`, client id `{thing_name}_{5 random digits}`. Subscribe to `dt/anker_power/A1761/{sn}/#`.
5. **Message envelope.** JSON `{"head": {..., "timestamp": N}, "payload": "<json string>"}`. The payload JSON has `data`, a base64 string that holds the hex frame.
6. **Hex frame.** `ff09`, 2-byte little-endian length (includes the trailing XOR byte), pattern `03010f`, 2-byte message type, an optional 1-byte counter, then fields, then an XOR checksum of all prior bytes.
   - Field: 1-byte name (`a2`, `a3`, ...), 1-byte length (2 bytes little-endian for JSON fields), then data. The first data byte is the value type when the length is greater than 2.
   - Value types (`DeviceHexDataTypes` in `apitypes.py`): `00` string, `01` unsigned byte, `02` signed 16-bit little-endian, `03` four bytes holding 1 to 4 values, and further types. The builder must read the whole enum and the field decoder in `mqtttypes.py`.
7. **Realtime trigger.** The device publishes type `0405` telemetry every 3 to 5 s only after a trigger command (`0057`) with fields `a2 = 1` and `a3 = timeout seconds`, plus a timestamp field `fe`. Valid timeouts are about 30 to 600 s. The exporter republishes it before the timeout ends. Publish topic: `cmd/anker_power/A1761/{sn}/req`. The command envelope is in `mqtt.py` (`publish`, around line 396). The builder must read it and `mqttcmdmap.py` `CMD_REALTIME_TRIGGER` to build the exact bytes.
8. **Type 0405 field map** (`_A1761_0405` in `mqttmap.py`): `a2` AC timeout s, `a3` DC timeout s, `a4` remaining hours (x0.1), `a5` AC input W, `a6` AC output W, `a7`/`a8` USB-C1/2 W, `a9`/`aa` USB-A1/2 W, `ae` DC input W, `af` solar W, `b0` total output W, `bb` AC status, `bc` charging status, `bd` temperature (signed), `be` expansion temperature, `c1` battery %, `c2` expansion %, `c3` health %, `c4` expansion health %, `c5` expansion count, `c6`..`c9` port status, `cc` 12 V status, `d0` serial, `d1` AC charge limit, `da` AC frequency, plus versions and settings. Type `0407` and `0830` also exist.

## Constraints and decisions

- Language: Go (owner's requirement).
- Credentials: the owner's main Anker account, from env vars or a mounted secret. The owner does not use the phone app, so a session clash is not a concern.
- Region: EU by default (builder's call), selectable by env var. The login response must name the right server if the owner picks the wrong one.
- Deployment: container image, run as a single replica. Which cluster and namespace is left to the owner.
- Metrics namespace `anker_c1000_*`, with a `device` label for the serial. This is the builder's call.
- Staleness: if no frame arrives for 2x the trigger interval, set `anker_up 0` and keep the last values out of the scrape (builder's call).

## Left to the builder

- Go module path, package layout, and the choice of MQTT client library.
- Exact metric names and help strings.
- How to cache the token (memory only is fine).
- Log format.

## Success criteria

1. The binary logs in and receives at least one `0405` frame from the real device.
2. `/metrics` shows battery %, input and output power, and temperatures with plausible values compared with the device display.
3. Unit tests decode captured frames to the expected values, and reject frames with a bad checksum.
4. The exporter survives a broker disconnect and a token expiry without a restart.
5. No secret appears in logs.

## Open risks

- The reference library lists the A1761 under "C1000X". The plain C1000 may differ in fields. The first live capture settles this.
- Anker can change the private API or block third-party clients at any time.
- Terms of service may forbid third-party API use.
