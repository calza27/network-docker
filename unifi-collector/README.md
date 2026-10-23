# unifi-collector

Receives alarm webhooks from UniFi Protect and exposes them as Prometheus metrics, counted per alarm and per camera.

```
UniFi Protect ──POST /metrics──▶ unifi-collector ◀──GET /metrics── Prometheus / Alloy
  (alarm webhook)                (in-memory counts)                  (scrape)
```

Each webhook increments a count for its alarm name and camera. Each scrape returns the counts gathered since the previous scrape, then resets them to zero. Nothing is held between scrapes, so restarting the container only loses events that arrived since the last scrape.

## Endpoints

Both endpoints are on the same path, `/metrics`, on the port set by `LISTEN_PORT`.

### `POST /metrics`: receive an event

Requires an `Authorization: Bearer <key>` header matching the configured auth key.

Only these fields of the body are used:

```json
{
  "alarm": {
    "name": "person_detected",
    "sources": [{ "device": "camera_1" }]
  }
}
```

- `alarm.name` becomes the metric name, so it must be a valid Prometheus metric name: letters, digits, `_` and `:`, not starting with a digit. Name alarms in UniFi Protect accordingly, e.g. `person_detected` rather than `Person Detected`.
- `alarm.sources[0].device` is looked up in `cameras.yml` to get the camera's friendly name. Cameras not listed there are recorded under their raw ID. Any further sources are ignored.

| Response | Meaning |
|---|---|
| `202 Accepted` | Event recorded |
| `400 Bad Request` | Body is not JSON, `alarm.name` is not a valid metric name, or `alarm.sources` is empty |
| `401 Unauthorized` | Missing or wrong bearer token |

Rejected events are also logged with the reason.

### `GET /metrics`: scrape

No authentication. Returns the counts since the last scrape in Prometheus text format, then clears them:

```
# HELP person_detected A count of the number of people detected
# TYPE person_detected gauge
person_detected{camera="back door"} 3
person_detected{camera="front door"} 1
```

If nothing has happened since the last scrape, the response is empty.

The metrics are gauges rather than counters because each value is a count for one scrape interval, not a running total. To get totals, add them up over a time range:

```promql
sum_over_time(person_detected[24h])                         # per camera, last 24h
sum by (camera) (sum_over_time(person_detected[$__range]))  # over the dashboard's time range
```

Because each scrape empties the counts, only one scraper should hit this endpoint. With two, each would receive only part of the events.

## Configuration

| Setting | Required | Description |
|---|---|---|
| `LISTEN_PORT` | Yes | Port to listen on |
| `AUTHKEY_FILE` | One of these | Path to a file containing the bearer token. Surrounding whitespace is trimmed. Takes precedence over `AUTHKEY`. |
| `AUTHKEY` | One of these | The bearer token itself |
| `/var/lib/cameras.yml` | Yes | Camera ID to friendly name map, read once at startup |

The container exits at startup if `LISTEN_PORT` is unset, no auth key is found, or `cameras.yml` is missing or invalid.

### cameras.yml

```yaml
---
camera_1: "front door"
camera_2: "back door"
```

The file is only read at startup, so restart the container after editing it.

### Metric descriptions

The `# HELP` text for each metric comes from the `metricHelp` map in `main.go`. Alarms not listed there get `A count of <name> events`. Add an entry when you add a new alarm in UniFi Protect if you want a better description.

## Deployment

The service runs from `docker-compose.yml`, which:

- publishes the collector on host port `30071` (container port `80`)
- mounts `/data/docker/mounts/unifi-collector/cameras.yml` as the camera map
- provides `/data/docker/secrets/unifi-collector/auth.key` as a Docker secret, read via `AUTHKEY_FILE`

The container runs as `1000:1000`, so both host files must be readable by that user:

```sh
sudo chown 1000:1000 /data/docker/secrets/unifi-collector/auth.key /data/docker/mounts/unifi-collector/cameras.yml
sudo chmod 600 /data/docker/secrets/unifi-collector/auth.key
```

To generate an auth key:

```sh
openssl rand -hex 32 | sudo tee /data/docker/secrets/unifi-collector/auth.key
```

Start it as you would any other service in this repo:

```sh
./docker.sh up unifi-collector
```

### UniFi Protect

In Protect's Alarm Manager, create an alarm with a webhook action:

- **URL:** `http://192.168.10.4:30071/metrics`
- **Method:** POST
- **Header:** `Authorization: Bearer <contents of auth.key>`

The alarm's name becomes the metric name, as described under [`POST /metrics`](#post-metrics-receive-an-event).

### Scraping

Add a job to `grafana/prometheus.yml`:

```yaml
  - job_name: 'unifi-collector'
    static_configs:
      - targets: ['192.168.10.4:30071']
```

## Testing by hand

```sh
curl -i -X POST http://192.168.10.4:30071/metrics \
  -H "Authorization: Bearer $(cat /data/docker/secrets/unifi-collector/auth.key)" \
  -d '{"alarm":{"name":"person_detected","sources":[{"device":"camera_1"}]}}'

curl http://192.168.10.4:30071/metrics
```

A scrape done by hand empties the counts just like a real one, so those events won't reach Prometheus.

## Development

```sh
go vet ./...
go test -race ./...
go build .
```

Tests are in `main_test.go`. `-race` matters: the handlers share `metricsCache`, and `TestConcurrentPostAndScrape` posts and scrapes at the same time to check no events are lost or counted twice.
