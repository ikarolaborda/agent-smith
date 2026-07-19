# Containment probe apparatus

This image is an opt-in hostile-worker test fixture, not a research adapter. It
exercises network denial, read-only mounts, secret isolation, capability drops,
orphan cleanup, hostile artifact types, and writable byte/inode ceilings through
the real Docker backend and broker.

Build it from an exact base identity, then run the live suite explicitly:

```sh
BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 \
  apparatus/containment-probe/build-image.sh
AGENT_SMITH_LIVE_CONTAINMENT_IMAGE=sha256:... \
  go test ./internal/research/runner -run TestLiveDockerContainment -v
```

The live suite may run against rootful Docker only as lower-assurance lab
evidence. Production support still requires rootless Docker or gVisor preflight.
