# Datadog DogStatsD-Only PoC

This proof-of-concept validates whether running lightweight DogStatsD-only agents on ephemeral Edge nodes can ship custom metrics without triggering per-host billing. 


## Quick Start (Single Node)

1. Copy the sample environment file and add your Datadog secrets.
   ```bash
   cp .env.example .env
   # edit .env and set DD_API_KEY=<trial_api_key> and optional overrides
   ```
   Keep `DD_HOSTNAME=edge-dogstatsd-poc-hostname` (or any shared value) so multiple nodes report under the same logical host.

   **Why these Datadog environment variables are required**
   - `DD_API_KEY`: authenticates the agent with your Datadog account; metrics are rejected if this is missing or wrong.
   - `DD_SITE`: points traffic to the right regional intake (e.g., `us5.datadoghq.com` for US5).
   - `DD_ENV`: attaches an `env` tag to any agent-side telemetry; using `poc` keeps this trial easy to filter.
   - `DD_SERVICE`: labels the DogStatsD sidecar so usage dashboards show a distinct service line item.
   - `DD_HOSTNAME`: overrides the host identity; by reusing the same value on every node we test whether host billing stays flat.
   - `DD_DOGSTATSD_NON_LOCAL_TRAFFIC`: allows UDP traffic from other containers/hosts. Even though the emitter runs on the same EC2 instance, it is in a separate container. Without this flag the agent only listens on 127.0.0.1 and would drop packets coming from bridge network IPs.
   - `DD_DOGSTATSD_PORT`: ensures both container and emitter agree on the UDP port (default 8125).
   - `DD_APM_ENABLED`, `DD_LOGS_ENABLED`, `DD_PROCESS_AGENT_ENABLED`, `DD_RUNTIME_METRICS_ENABLED`: set to `false` so only DogStatsD runs; keeps infra metrics and host signals suppressed.
   - `DD_HISTOGRAM_PERCENTILES`, `DD_HISTOGRAM_AGGREGATES`: enables server-side percentile/aggregate series for the histogram/distribution metrics we emit.

2. Launch DogStatsD in detached mode.
   ```bash
   docker compose up -d dogstatsd
   ```

3. Build and run the emitter container (about 10 minutes of traffic by default).
   ```bash
   docker compose up --build emitter
   ```
   The emitter streams metrics every 5 seconds for 120 cycles and then exits. Metrics arrive within ~60 seconds in Datadog → Metrics Summary. No infrastructure metrics are generated, and the usage page should continue to show a single host.
   - To rerun another batch later, use `docker compose run --rm emitter`.
   - To keep DogStatsD alive between runs, leave the `dogstatsd` service running (`docker compose ps` to check status).

## Multi-Node Verification Workflow

Observe Datadog:
   - **Metrics** → `core_job_events_total`, `scanner_job_runtime_seconds`, etc. verify counts and percentiles.
   - **Usage > Hosts**: the `Infrastructure list` and `Host usage` charts should show a single host (matching `DD_HOSTNAME`).
   - **Usage > Metrics**: confirm custom metric count (expected < 500 for this suite).
Optional: change `DD_HOSTNAME` to a unique value on one node and confirm that Datadog bills an additional host, then revert to shared hostname.

## Emitter Configuration

The emitter container ships with safe defaults: stream metrics every 5 seconds for 120 cycles (~10 minutes). Tweak behaviour with environment variables either in `.env` or ad-hoc when invoking `docker compose run`.

- `DOGSTATSD_HOST` / `DOGSTATSD_PORT`: where to send UDP metrics (default `dogstatsd:8125` inside the compose network).
- `EMIT_ITERATIONS`: number of emission cycles (`120` by default, `0` means run until interrupted).
- `EMIT_INTERVAL`: delay between cycles (default `5s`).
- `EMIT_NAMESPACE`: metric namespace prefix (`edge_nodes.`).
- `EMIT_TAGS`: comma-separated base tags (keep to approved enum tags to avoid cardinality blow-ups).

Examples:

```bash
# Continuous stream until Ctrl+C
docker compose run --rm -e EMIT_ITERATIONS=0 emitter

# Faster cadence load test
docker compose run --rm -e EMIT_INTERVAL=1s -e EMIT_ITERATIONS=600 emitter

# Custom tag set for a specific scenario
docker compose run --rm -e EMIT_TAGS=service:edge-scanner,product:fastscan,priority:high,region:eu-west-1 emitter
```

Metrics emitted:
- Counters: `core_job_events_total`, `core_jobs_dispatched_total`, `core_sqs_polls_empty_total`, `scanner_jobs_started_total`, `scanner_chain_step_total`, `scanner_artifacts_uploaded_total`, `scanner_artifacts_upload_failed_total`, `core_result_uploads_reported_total`, `core_dead_letter_messages_total`, `core_webhook_delivery_total`
- Gauge: `core_queue_depth`
- Histogram/Distribution: `scanner_job_runtime_seconds`, `scanner_chain_step_duration_seconds`

No `host`, `container_id`, or other high-cardinality tags are attached.

## Fresh EC2 Bootstrap 

- Update packages:
  ```bash
  sudo apt-get update && sudo apt-get install -y ca-certificates curl gnupg lsb-release
  ```
  (Use `sudo yum update -y` on Amazon Linux.)
- Install Docker Engine:
  ```bash
  curl -fsSL https://get.docker.com | sh
  sudo usermod -aG docker $USER
  newgrp docker
  docker version
  ```
- Install Docker Compose plugin (v2):
  ```bash
  DOCKER_CONFIG=${DOCKER_CONFIG:-$HOME/.docker}
  mkdir -p "$DOCKER_CONFIG/cli-plugins"
  curl -SL https://github.com/docker/compose/releases/download/v2.24.6/docker-compose-linux-$(uname -m) \
    -o "$DOCKER_CONFIG/cli-plugins/docker-compose"
  chmod +x "$DOCKER_CONFIG/cli-plugins/docker-compose"
  docker compose version
  ```
- Clone this project, populate `.env`, then follow the Quick Start steps.

## Verification Checklist

- Metrics land: Datadog → Metrics → Summary → search `core_job_events_total`.
- Custom metric count: Datadog → Usage → Metrics → filter by tag `service:edge-scanner`.
- Host billing: Datadog → Usage → Hosts (and Infrastructure list). Expect a single host named after `DD_HOSTNAME` no matter how many nodes participate.
- Percentiles: Graph `scanner_job_runtime_seconds{service:edge-scanner}` with p95/p99 to confirm distribution accuracy.

## Tuning & Diagnostics

- To emit from containers or different hosts, ensure UDP/8125 is reachable. `DD_DOGSTATSD_NON_LOCAL_TRAFFIC=true` is already configured.
- Check container logs with `docker compose logs -f dogstatsd` for connection or API errors.
- To simulate per-host billing, set unique `DD_HOSTNAME` per node and compare the Usage page, then revert to the shared hostname.
- When done testing: `docker compose down` and stop the emitter processes.
