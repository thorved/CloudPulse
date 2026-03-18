# CloudPulse

CloudPulse is a small Go service that keeps one Cloudflare hostname aligned with the healthy subset of a configured set of IPv4 targets.

## What It Does

- Reads a local `config.json`
- Pings each enabled target on a fixed interval
- Removes a target's Cloudflare `A` record after `failure_threshold` consecutive failures
- Re-adds a target's `A` record after `recovery_threshold` consecutive successes
- Treats the configured hostname as authoritative and fixes manual Cloudflare drift
- Preserves one last-known-active record by default when all targets are unhealthy

CloudPulse v1 is intentionally scoped to:

- One hostname
- Multiple IPv4 targets
- ICMP health checks only
- Cloudflare `A` records only
- In-memory runtime state only

## Commands

```bash
go run ./cmd/cloudpulse run -config config.json
go run ./cmd/cloudpulse dry-run -config config.json
go run ./cmd/cloudpulse once -config config.json
go run ./cmd/cloudpulse validate -config config.json
```

Command behavior:

- `run`: continuous checks with Cloudflare mutations enabled
- `dry-run`: continuous checks with intended DNS actions logged only
- `once`: runs a single health-check and reconciliation cycle
- `validate`: validates config and confirms Cloudflare access with a non-mutating list call

## Config

See `config.example.json` for a complete example.

Important config rules:

- `dns.record_type` must be `A`
- `checks.method` must be `icmp`
- Targets must be unique public IPv4 addresses
- `timeout_seconds` must be lower than `interval_seconds`
- At least one target must be enabled
- `cloudflare.zone_id` is optional; when omitted, CloudPulse resolves the zone from `dns.name`
- The API token should have at least `Zone DNS Read` for `validate` and `Zone DNS Edit` for `run`, `dry-run`, and `once`
- If automatic zone discovery cannot find an accessible matching zone, set `cloudflare.zone_id` explicitly

## Reconciliation Rules

CloudPulse manages the configured hostname authoritatively for `A` records:

- Missing desired records are created
- Duplicate configured records are deleted
- Records with the wrong `ttl` or `proxied` settings are updated
- Unexpected `A` records at the managed hostname are deleted
- Non-`A` records at the same hostname are left alone

Safety behavior:

- If all enabled targets are unhealthy and `allow_zero_records` is `false`, CloudPulse keeps exactly one currently active target in DNS
- If no managed records exist at startup and all targets are unhealthy, CloudPulse leaves DNS empty until a target meets the recovery threshold

## Restart Behavior

On startup, CloudPulse reads the current Cloudflare records for the managed hostname and seeds runtime state from the actual DNS set before the first cycle runs.

## Development

```bash
go test ./...
go build ./...
```

## Docker

Build the container image:

```bash
docker build -t cloudpulse:local .
```

Run it directly:

```bash
docker run --rm \
  --cap-add NET_RAW \
  -v "$(pwd)/config.json:/app/config.json:ro" \
  cloudpulse:local
```

Use Docker Compose:

```bash
docker compose up --build -d
docker compose logs -f cloudpulse
```

The compose file mounts `./config.json` into the container and grants `NET_RAW` so ICMP checks can run inside Docker.
It relies on the image default command, which already starts `cloudpulse run -config /app/config.json`.

For a production host that should pull a published image instead of building locally:

```bash
docker compose -f docker-compose.prod.yml pull
docker compose -f docker-compose.prod.yml up -d
docker compose -f docker-compose.prod.yml logs -f cloudpulse
```

The production compose file uses `ghcr.io/thorved/cloudpulse:latest`, so it is meant to be used with the GitHub Actions image-publish workflow below.

## GitHub Actions

The repository includes a CI workflow at `.github/workflows/ci.yml` that:

- runs `go test ./...`
- runs `go build ./...`
- builds the Docker image to catch container build regressions
- publishes `ghcr.io/thorved/cloudpulse:latest` on pushes to `main` or `master`
- can also be run manually with GitHub Actions `workflow_dispatch`

## Notes

- On Windows, CloudPulse enables the ICMP library's privileged mode so ping works reliably without redesigning the checker later.
- On Linux and other Unix-like systems, ICMP behavior depends on local raw-socket or ping capability support.
