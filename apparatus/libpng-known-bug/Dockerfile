ARG BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
FROM ${BASE_IMAGE}

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       ca-certificates clang coreutils findutils jq libclang-rt-14-dev \
       llvm patch zlib1g-dev \
    && rm -rf /var/lib/apt/lists/* \
    && clang++ --version \
    && llvm-symbolizer --version

COPY apparatus/libpng-known-bug/dispatch /apparatus/dispatch
COPY apparatus/libpng-known-bug/harness_png.cc /apparatus/harness_png.cc
COPY apparatus/libpng-known-bug/png.dict /apparatus/png.dict
COPY apparatus/libpng-known-bug/seed.png.b64 /apparatus/seed.png.b64
RUN chmod 0555 /apparatus/dispatch \
    && chmod 0444 /apparatus/harness_png.cc /apparatus/png.dict /apparatus/seed.png.b64 \
    && mkdir -p /work \
    && chmod 0555 /work

WORKDIR /work
CMD ["/apparatus/dispatch", "list_harnesses"]
