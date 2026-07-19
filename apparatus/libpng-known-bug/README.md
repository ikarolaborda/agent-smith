# libpng known-bug benchmark apparatus

This opt-in offline adapter calibrates the research pipeline against the public
libpng vulnerability CVE-2025-64720 (GHSA-hfc7-ph9c-wcww). The upstream
advisory identifies versions before 1.6.51 as affected and cites initial fix
commit `08da33b4c88cfcd36e5a706558a8d7e0e4773643`. Upstream subsequently
documented that a gamma/palette flag-synchronization case still reached the
same out-of-bounds read, then added a defensive fix in
`788a624d7387a758ffd5c7ab010f1870dea753a1` and the final correctness fix in
`a05a48b756de63e3234ea6b3b938b8f5f862484a`. Both are included in 1.6.52.

The checked-in calibration harness uses libpng's simplified API to request RGB
output from an alpha-bearing palette image with a null background. That reaches
the affected local-compositing path. Its destination buffer is initialized to
component value 190, matching the failing state published in upstream issue
#686. The harness hash is recorded in inspection and build provenance.

The worker never downloads source. Capture an authorized exact source tree for
each revision first. The reference revisions are:

- vulnerable `v1.6.50`: `2b978915d82377df13fcbb1fb56660195ded868a`
- fixed `v1.6.52`: `fbed16182b92eeb3a06d96e49f0836d450318098`

Do not use 1.6.51 as this apparatus's negative control. The checked-in public
input still produces the `png_image_read_composite` ASan finding there; the
1.6.52 control proves absence after upstream's finalized fix.

Builds use libpng's checked-in `scripts/pnglibconf.h.prebuilt` and compile the
portable core sources directly. This avoids generated symlinks and keeps every
transient build inode visible to the worker quota monitor. The current manifest
therefore declares amd64 only.

Build the pinned apparatus image:

```sh
BASE_IMAGE=docker.io/library/debian:bookworm-slim@sha256:7b140f374b289a7c2befc338f42ebe6441b7ea838a042bbd5acbfca6ec875818 \
  apparatus/libpng-known-bug/build-image.sh
```

The checked-in reproducer is the base64 form of the upstream `issue-1.zip`
crash input and has decoded SHA-256
`fdeb6ef7e80ebebd2b92d2fdb6855073dce06b7d5e1d27012532dd738cfaa595`.
It is benchmark evidence only: seeding it into a campaign must never be counted
as autonomous discovery, and the known CVE must never be labelled novel.

Upstream references:

- advisory: <https://github.com/pnggroup/libpng/security/advisories/GHSA-hfc7-ph9c-wcww>
- report and public input: <https://github.com/pnggroup/libpng/issues/686>
- initial fix: <https://github.com/pnggroup/libpng/commit/08da33b4c88cfcd36e5a706558a8d7e0e4773643>
- defensive follow-up: <https://github.com/pnggroup/libpng/commit/788a624d7387a758ffd5c7ab010f1870dea753a1>
- finalized fix: <https://github.com/pnggroup/libpng/commit/a05a48b756de63e3234ea6b3b938b8f5f862484a>
