# Running segcheck in a container

The image is one static binary and a CA bundle: no shell, no package manager,
non-root, a few megabytes. It is the same binary as the release archive for the
same version — goreleaser packages the artefact it already built rather than
rebuilding it.

```bash
docker run --rm ghcr.io/allan-nava/segcheck:latest \
  check https://cdn.example/master.m3u8
```

Multi-arch: `linux/amd64` and `linux/arm64` resolve from the same tag.

| Tag | What it is |
|---|---|
| `vX.Y.Z` | A release. Built by goreleaser from the same binaries as the release archives. Pin this. |
| `latest` | The newest release, and it stays put when the tag is a prerelease. |
| `edge` | The head of `main`, rebuilt on every push. Useful for trying a fix before it ships; not for a schedule. |
| `sha-<commit>` | One specific commit of `main`, immutable. |

An `edge` image has passed the same contract test as a release — the publish is
gated on it — but it has not been through a release's real-stream verification.

## Building it yourself

```bash
docker build -t segcheck --build-arg VERSION="$(git describe --tags)" .
docker run --rm segcheck check https://cdn.example/master.m3u8
```

`--build-arg VERSION` matters: without it the binary reports `dev`, and a report
that cannot say which build produced it is a report you cannot act on later.

## What the image deliberately does not have

- **No shell.** `docker exec … sh` will not work, and neither will an attacker
  who gets that far. If you need to debug, run the binary with different flags.
- **No writable filesystem needs.** Run it with `--read-only` and it behaves.
- **No root.** It runs as UID 65532.

The image carries `/etc/ssl/certs/ca-certificates.crt` and nothing else besides
the binary. That file is the one thing a `FROM scratch` image is easy to forget:
without it every `https://` manifest fails TLS, and the failure reads exactly
like a broken origin. `internal/analyze/docker_test.go` asserts it is there.

## Docker Compose — a check on a schedule

Compose has no scheduler, so this is the shape for an ad-hoc run or for a
sidecar you `docker compose run` from cron:

```yaml
services:
  segcheck:
    image: ghcr.io/allan-nava/segcheck:latest
    read_only: true
    command:
      - check
      - https://cdn.example/master.m3u8
      - --segments=6
      - --output=json
    # Credentials come from the environment and are assembled into a header by
    # the caller — never as a literal on the command line, where they land in
    # `docker inspect`, shell history and CI logs.
    environment:
      CDN_TOKEN: ${CDN_TOKEN}
```

To pass that token as a header, wrap the invocation:

```bash
docker run --rm -e CDN_TOKEN ghcr.io/allan-nava/segcheck:latest \
  check https://cdn.example/master.m3u8 --header "Authorization: Bearer $CDN_TOKEN"
```

Note the shell expands `$CDN_TOKEN` on the host before `docker run` sees it, so
the value does reach `docker inspect`. If that matters in your environment, mount
a wrapper script instead — or wait for a config file (SC-31).

## Kubernetes — a CronJob per stream

```yaml
apiVersion: batch/v1
kind: CronJob
metadata:
  name: segcheck-main-ladder
spec:
  schedule: "*/15 * * * *"
  concurrencyPolicy: Forbid
  successfulJobsHistoryLimit: 3
  jobTemplate:
    spec:
      # segcheck exits 0 whenever the check ran, so a non-zero exit means the
      # run itself broke — not that the stream is bad. One retry, no more.
      backoffLimit: 1
      template:
        spec:
          restartPolicy: Never
          containers:
            - name: segcheck
              image: ghcr.io/allan-nava/segcheck:v0.3.0
              args:
                - check
                - https://cdn.example/master.m3u8
                - --segments=6
                - --renditions=3
                - --output=json
              resources:
                requests: { cpu: 100m, memory: 64Mi }
                limits: { cpu: "1", memory: 256Mi }
              securityContext:
                allowPrivilegeEscalation: false
                readOnlyRootFilesystem: true
                runAsNonRoot: true
                capabilities: { drop: ["ALL"] }
```

Pin the tag to a version rather than `latest`: a scheduled check whose behaviour
changes underneath you produces findings you cannot compare against last week's.

### About `--exit-on`

Leave it off here. segcheck exits 0 whenever the check ran, findings or not, and
a `CronJob` reads a non-zero exit as "the job failed" — which would retry the
run, mark the job failed in every dashboard, and tell you nothing about the
stream. Ship the findings instead: parse the JSON, or wait for the Prometheus
(SC-27) and Slack (SC-28) outputs.

Use `--exit-on bad` only where a non-zero exit is genuinely what you want to
signal — a CI job gating a release on a stream being sane.

## Cost

Each run downloads `renditions × segments` segments plus one initialisation
segment per rendition. A five-rung 1080p ladder at the defaults is roughly
100–200 MB per run; at `*/15` that is around 20 GB a day of egress from your
CDN. Trim it with `--renditions` and `--segments` before pointing a schedule at
production.
