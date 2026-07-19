ARG BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
FROM ${BASE_IMAGE}

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl jq coreutils findutils \
    && rm -rf /var/lib/apt/lists/*

COPY apparatus/containment-probe/dispatch /apparatus/dispatch
RUN chmod 0555 /apparatus/dispatch
