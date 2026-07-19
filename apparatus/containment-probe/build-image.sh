#!/bin/sh
set -eu

base_image="${BASE_IMAGE:-}"
base_digest="${base_image##*@sha256:}"
if [ "$base_digest" = "$base_image" ] || [ "${#base_digest}" -ne 64 ]; then
    echo "BASE_IMAGE must be pinned as repository@sha256:<64 lowercase hex characters>" >&2
    exit 64
fi
case "$base_digest" in *[!0-9a-f]*) echo "BASE_IMAGE digest must be lowercase hexadecimal" >&2; exit 64 ;; esac

tag="${APPARATUS_TAG:-agent-smith/containment-probe:local}"
docker build --build-arg BASE_IMAGE="$base_image" -f apparatus/containment-probe/Dockerfile -t "$tag" .
image_digest="$(docker image inspect --format '{{.Id}}' "$tag")"
case "$image_digest" in
    sha256:????????????????????????????????????????????????????????????????) ;;
    *) echo "Docker did not return an exact image ID" >&2; exit 1 ;;
esac
printf '%s\n' "$image_digest"
