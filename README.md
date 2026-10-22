# anker-exporter

A Prometheus exporter for the Anker SOLIX C1000 (model A1761) power station.

The exporter logs in to the Anker cloud, connects to the Anker MQTT broker and turns the device telemetry into metrics. It does not talk to the device directly.

## Run

```sh
export ANKER_EMAIL=you@example.com
export ANKER_PASSWORD=...
go run ./cmd/anker-exporter
```

Or use the container image:

```sh
docker build -t anker-exporter:dev .
docker run --rm -e ANKER_EMAIL -e ANKER_PASSWORD -p 9876:9876 anker-exporter:dev
```

The login is slow, about 22 seconds in testing. The metrics appear after the first telemetry frame arrives.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `ANKER_EMAIL` | required | Anker account email. |
| `ANKER_PASSWORD` | required | Anker account password. |
| `ANKER_REGION` | `eu` | API region: `eu` or `com`. |
| `ANKER_COUNTRY` | `GB` | Country code sent at login. |
| `ANKER_DEVICE_SN` | empty | Device serial. If empty, the exporter uses the only A1761 on the account. It stops if there are none or several. |

| Flag | Default | Meaning |
|---|---|---|
| `-listen` | `:9876` | HTTP listen address. |
| `-trigger` | `300s` | Realtime trigger timeout. The exporter resends the trigger before it expires. |
| `-stale-after` | `30s` | `anker_c1000_up` goes to 0 when no telemetry frame arrived for this long. |
| `-print-metrics` | off | Print the metric names and help text, then exit. |

## Endpoints

- `/metrics` serves the device metrics, plus the Go and process metrics.
- `/healthz` returns 200 while the process runs. It does not reflect the login state. Use `anker_c1000_up` for that.

## Metrics

Every device metric carries a `device` label with the serial. Run `anker-exporter -print-metrics` for the full list with help text.

- `anker_c1000_up`: 1 when a frame arrived within `-stale-after`.
- `anker_c1000_last_message_age_seconds`: age of the last frame.
- `anker_c1000_bad_frames_total`: frames that failed to decode.
- `anker_c1000_info`: always 1. The labels carry the serial and firmware versions.
- Battery: `main_battery_soc_percent`, `battery_soh_percent`, `temperature_celsius`, and the `exp_1_*` equivalents for an expansion pack.
- Power in watts: `ac_input_power_watts`, `ac_output_power_watts`, `dc_input_power_watts`, `photovoltaic_power_watts`, `output_power_total_watts`, and `usbc_1`, `usbc_2`, `usba_1`, `usba_2` power.
- Status and settings: port states, switches, modes, timeouts and limits, all prefixed with `anker_c1000_`.

When a frame is older than `-stale-after`, the exporter drops all device metrics except `up`, `last_message_age_seconds` and `bad_frames_total`.

## Warning: remaining time is unverified

`anker_c1000_remaining_time_hours` is the device estimate. Its meaning is unverified. Raw values reached 6634 at 100% charge. Do not alert on it. The exporter omits the metric when the device reports the "unknown" value.

## Risks

- The exporter uses a private Anker API. Anker can change it or block it without notice.
- Using the private API can conflict with the Anker terms of service. You must check the terms and decide for yourself.
- The exporter stores your account password in its environment. Keep it out of images, logs and version control.
- The cloud login is slow.
- If Anker rejects the login, the exporter retries after 5 minutes and doubles the delay up to 1 hour. After 5 rejections in a row it exits with an error, so repeated bad logins cannot lock your account.
- After a network error or timeout, the exporter retries after 30 seconds and doubles the delay up to 5 minutes.
- While setup fails, `/healthz` stays up and the device metrics are absent.
- The exporter sends one command to the device: the realtime telemetry trigger. It sends no other command.
