ARG BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818
FROM ${BASE_IMAGE}

RUN apt-get update \
    && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
       ca-certificates clang llvm jq libclang-rt-14-dev patch \
    && rm -rf /var/lib/apt/lists/* \
    && clang++ --version \
    && llvm-symbolizer --version

COPY apparatus/native-clang/dispatch /apparatus/dispatch
RUN chmod 0555 /apparatus/dispatch \
    && mkdir -p /work \
    && chmod 0555 /work

WORKDIR /work
CMD ["/apparatus/dispatch", "list_harnesses"]
