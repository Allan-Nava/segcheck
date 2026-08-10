# syntax=docker/dockerfile:1
#
# segcheck in a container: a static binary and a trust store, nothing else.
#
# This is the image built from a clone (`docker build -t segcheck .`) and the one
# CI smoke-tests. Released multi-arch images are built by goreleaser from
# Dockerfile.release, which starts from prebuilt binaries; both must satisfy the
# same contract — no shell, non-root, working TLS — asserted by
# internal/analyze/docker_test.go.

FROM golang:1.25 AS build

WORKDIR /src

# There is no `go mod download` step because there is nothing to download:
# go.mod has no require block and go.sum stays empty, and CI enforces both.
# Copying the tree *is* the dependency step.
COPY . .

# VERSION is stamped into the same variable goreleaser stamps, so an image and a
# released binary can never disagree about what they are.
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/segcheck ./cmd/segcheck

FROM scratch

# scratch has no trust store. Without this, every https:// manifest fails TLS
# inside the container and the failure looks like a broken origin rather than a
# broken image — exactly the phantom this tool exists not to produce.
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/segcheck /segcheck

# Numeric, because scratch has no /etc/passwd to resolve a name against.
USER 65532:65532

ENTRYPOINT ["/segcheck"]
CMD ["--help"]

LABEL org.opencontainers.image.title="segcheck" \
      org.opencontainers.image.description="Check what HLS/DASH segments really contain, not just what the manifest says" \
      org.opencontainers.image.source="https://github.com/Allan-Nava/segcheck" \
      org.opencontainers.image.licenses="PolyForm-Noncommercial-1.0.0"
