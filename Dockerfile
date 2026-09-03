# ------------------------------------------------------------------------------
# Stage 1 — build the statically linked service binary
# ------------------------------------------------------------------------------
# Pinned to the build platform so the Go toolchain always runs natively and
# cross-compiles for TARGETARCH. Emulating the compiler under QEMU would make
# the arm64 build several times slower for no benefit.
FROM --platform=${BUILDPLATFORM} golang:1.24-alpine AS build

# CGO is unused (the only platform call, unix.Statfs, comes from
# golang.org/x/sys), so the binary links statically and needs no libc at
# runtime. GOWORK=off mirrors the Makefile: builds resolve from go.mod alone.
# GOTOOLCHAIN=local fails loudly rather than fetching a toolchain mid-build.
ENV CGO_ENABLED=0 \
    GOWORK=off \
    GOTOOLCHAIN=local

WORKDIR /src

# Dependency layer, invalidated only when the module graph changes.
COPY go.mod go.sum ./
RUN go mod download

# Copy only what the service binary needs. data/ and .env hold live key
# material and private keys; neither reaches the build context (.dockerignore).
COPY cmd/ ./cmd/
COPY internal/ ./internal/

ARG TARGETOS
ARG TARGETARCH
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false -ldflags='-s -w' \
        -o /out/co-signer ./cmd/co-signer

# The share stores must exist and be private before startup: sharestore rejects
# any directory carrying a group or other permission bit. They are staged here
# because the runtime image has no shell to mkdir/chmod with.
RUN mkdir -p /state/primary /state/recovery \
    && chmod 0700 /state/primary /state/recovery

# ------------------------------------------------------------------------------
# Stage 2 — runtime
# ------------------------------------------------------------------------------
# distroless static: no shell, no package manager, no libc — just CA
# certificates (needed for HTTPS calls to the monolith), tzdata and a
# pre-declared non-root user.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime

LABEL org.opencontainers.image.source="https://github.com/BroLabel/brosettlement-mpc-co-signer" \
      org.opencontainers.image.description="BroSettlement MPC co-signer" \
      org.opencontainers.image.licenses="Apache-2.0"

# Binary stays root-owned and read-only to the running user.
COPY --from=build /out/co-signer /usr/local/bin/co-signer

# 65532 is the distroless "nonroot" uid/gid; numeric to avoid name lookups.
COPY --from=build --chown=65532:65532 /state/ /var/lib/co-signer/

# Defaults matching the staged directories. Mount durable storage over both in
# production — an image path alone does not survive container replacement.
ENV CO_SIGNER_PRIMARY_SHARES_DIR=/var/lib/co-signer/primary \
    CO_SIGNER_RECOVERY_SHARES_DIR=/var/lib/co-signer/recovery

# Health, readiness and /metrics. config.httpAddr() defaults to 0.0.0.0:8081
# and honours CO_SIGNER_HTTP_ADDR or PORT.
EXPOSE 8081

# No HEALTHCHECK: the image intentionally ships no shell or HTTP client. Probe
# the port above from the orchestrator instead (non-ready returns HTTP 503).

USER 65532:65532
ENTRYPOINT ["/usr/local/bin/co-signer"]
