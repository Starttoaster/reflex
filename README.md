# reflex

reflex is an [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) webhook receiver that sends each alert group to one of two Discord channels:

- **urgent**: someone needs to act now.
- **info**: it can wait.

It decides by asking [Jev](https://docs.typesafe.ai), TypeSafe's System One model, a single Choice question about the alert group. If Jev is slow, unreachable, unconfigured or unsure, reflex falls back to label routing: `severity="critical"` is urgent and everything else is info. Delivery never depends on Jev.

## How it routes

For each firing notification, reflex:

1. Builds a structured summary of the group: alertname, severity, each alert's `title` and `description` annotations, how long each alert has been firing, which hosts are affected, and how many alerts are firing or resolved.
2. Redacts private IPv4 addresses (and any patterns or labels you configure) from that summary.
3. Asks Jev to choose `page now` or `informational`, plus a 0–4 urgency score that is only logged.
4. Picks the channel:

| Jev result | Channel | Reason |
| --- | --- | --- |
| confidence ≥ `--min-confidence` and P(page now) ≥ `--threshold` | urgent | `jev` |
| confidence ≥ `--min-confidence` and P(page now) < `--threshold` | info | `jev` |
| confidence < `--min-confidence` | label rule | `fallback_low_confidence` |
| no answer within `--jev-timeout` | label rule | `fallback_timeout` |
| HTTP error, bad response | label rule | `fallback_error` |
| `JEV_API_KEY` not set | label rule | `fallback_no_key` |

Resolved notifications go to whichever channel the group's firing notification used (`sticky`). reflex keeps that mapping in memory; if it doesn't know the group (for example after a restart, or because another replica handled the firing notification), it uses the label rule (`fallback_unknown_group`).

With two options, Jev's Choice confidence is `2 × max(p) − 1`, so the default `--min-confidence 0.6` means "one option has at least 80%".

## Discord messages

Messages are embeds titled `[FIRING:n] <alertname>` or `[RESOLVED] <alertname>`. The body lists each firing alert as a bold title with its description, then resolved alerts struck through. The footer shows the routing reason, for example `routed by Jev (p=0.93)` or `fallback (Jev timeout): severity=critical`. Titles and bodies are truncated to Discord's limits, dropping whole alerts and noting how many were left out. Mentions in alert text never ping anyone.

## Usage

```sh
export JEV_API_KEY=...                 # optional; without it reflex routes by labels only
export DISCORD_URGENT_WEBHOOK=https://discord.com/api/webhooks/...
export DISCORD_INFO_WEBHOOK=https://discord.com/api/webhooks/...
reflex serve
```

| Endpoint | Purpose |
| --- | --- |
| `POST /alerts` | Alertmanager webhook receiver |
| `GET /healthz` | liveness and readiness |
| `GET /metrics` | Prometheus metrics |

If Discord rejects a message, reflex returns `502` so Alertmanager retries the notification.

### Dry runs

`reflex classify` reads a saved Alertmanager payload, asks Jev about it and prints the state sent, Jev's response and the decision, without sending anything to Discord. Use it to tune the threshold against real payloads:

```sh
reflex classify -f payload.json
reflex classify --threshold 0.9 -f - < payload.json
```

### Configuration

Secrets are read from the environment only: `JEV_API_KEY`, `DISCORD_URGENT_WEBHOOK` and `DISCORD_INFO_WEBHOOK`. Everything else is a flag, and every flag can also be set with an environment variable. A flag on the command line wins over the environment.

| Flag | Environment | Default | |
| --- | --- | --- | --- |
| `--listen` | `REFLEX_LISTEN` | `:8080` | listen address (serve) |
| `--threshold` | `REFLEX_THRESHOLD` | `0.8` | minimum P(page now) for urgent |
| `--min-confidence` | `REFLEX_MIN_CONFIDENCE` | `0.6` | minimum confidence to trust Jev |
| `--jev-timeout` | `REFLEX_JEV_TIMEOUT` | `1.5s` | time limit for the Jev call |
| `--jev-endpoint` | `JEV_ENDPOINT` | `https://api.typesafe.ai/v1/systemone` | |
| `--jev-model` | `JEV_MODEL` | `jev-latest` | |
| `--scrub-private-ips` | `REFLEX_SCRUB_PRIVATE_IPS` | `true` | redact RFC 1918 and loopback IPv4 addresses |
| `--scrub-pattern` | `REFLEX_SCRUB_PATTERNS` | | extra regex to redact; repeatable, one per line in the env var |
| `--questions-file` | `REFLEX_QUESTIONS_FILE` | | YAML wording for the Jev questions; see below |
| `--scrub-label` | `REFLEX_SCRUB_LABELS` | | labels to leave out entirely; comma-separated |
| `--sticky-ttl` | `REFLEX_STICKY_TTL` | `168h` | how long to remember a firing group's channel (serve) |
| `--send-timeout` | `REFLEX_SEND_TIMEOUT` | `10s` | time limit per Discord delivery (serve) |
| `--log-format` | `REFLEX_LOG_FORMAT` | `json` | `json` or `text` (serve) |
| `--log-level` | `REFLEX_LOG_LEVEL` | `info` | (serve) |

### Customising the questions

The wording sent to Jev is the main lever for tuning decisions to your environment. Put your changes in a YAML file and pass it with `--questions-file`; any field you leave out keeps the default. Print the defaults as a starting point with:

```sh
reflex questions > questions.yaml
```

```yaml
# Optional: describe your environment. Added to the state as "context".
context: >-
  Single-operator homelab. The media server and test VMs can wait until evening;
  backups, DNS and the NAS cannot.
route:
  instructions: Should this alert group page the operator now, or can it wait?
  page_now: A core service is down, data or backups are at risk, or there is a security problem.
  informational: Anything else, including media server issues and brief blips.
urgency:
  enabled: true            # logged-only score; set false to skip it
  levels: [No action needed, This week, Today, Within the hour, Now]   # 2 to 10, least urgent first
```

The structure is fixed: one choice between `page now` and `informational`, and an optional urgency score. Unknown keys are rejected, so a typo fails at startup instead of silently using a default. Try a new wording against saved payloads with `reflex classify --questions-file questions.yaml -f payload.json` before deploying it.

### Privacy

Alert content is sent to TypeSafe's API. Only the summary described above is sent, after scrubbing; labels other than alertname, severity, group labels and the host are not included. Add `--scrub-pattern` for anything else you consider sensitive, such as internal domain names, and `--scrub-label` to leave host names out.

## Alertmanager configuration

```yaml
receivers:
  - name: reflex
    webhook_configs:
      - url: http://reflex.monitoring.svc:8080/alerts
        send_resolved: true
```

With the Prometheus Operator, use a `webhookConfigs` receiver in an `AlertmanagerConfig`. To trial reflex next to an existing receiver, add it as a route with `continue: true` and compare the `channel` and `label_channel` fields in its logs.

## Observability

| Metric | Labels |
| --- | --- |
| `reflex_notifications_received_total` | `status` |
| `reflex_alerts_received_total` | `status` |
| `reflex_routing_decisions_total` | `channel`, `reason` |
| `reflex_jev_request_duration_seconds` | |
| `reflex_discord_send_failures_total` | `channel` |
| `reflex_invalid_payloads_total` | |

Each decision is logged with `channel`, `reason`, `p_page`, `confidence`, `urgency`, `jev_latency_ms` and `label_channel` (what label routing alone would have chosen).

## Container image

```sh
docker run --rm -p 8080:8080 \
  -e JEV_API_KEY -e DISCORD_URGENT_WEBHOOK -e DISCORD_INFO_WEBHOOK \
  ghcr.io/starttoaster/reflex:<version>
```

Images are built for `linux/amd64` and `linux/arm64` on a distroless base and run as a non-root user.

## Development

```sh
go test -race ./...
go vet ./...
golangci-lint run
```

Test payloads in `testdata/` are invented; please keep real hostnames, addresses and alert text out of the repository.

## License

[MIT](LICENSE)
