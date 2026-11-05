# syntax=docker/dockerfile:1

# Build static, ship tiny: the runtime stage has no shell, no package manager
# and no libc to keep in sync. Keep the Go tag in step with go.mod.
FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
# TARGETARCH is set by buildx for multi-arch builds; a plain `docker build`
# leaves it empty, and the builder already runs on the target arch.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
      go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/cartesia-gw ./cmd/cartesia-gw

FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev
LABEL org.opencontainers.image.title="cartesia-gw" \
      org.opencontainers.image.description="OpenAI-compatible Cartesia TTS gateway with key rotation and a dashboard" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.source="https://github.com/rizkirmdhnnn/cartesia-gw"

COPY --from=build /out/cartesia-gw /cartesia-gw

# /data must exist before the VOLUME declaration and already belong to the
# nonroot user (65532). Without this line Docker creates the volume directory
# as root:root on first use, and the process cannot create data.sqlite — the
# failure mode is "ping sqlite /data/data.sqlite: unable to open database file
# (14)" followed by a restart loop.
COPY --from=build --chown=nonroot:nonroot /out /data

# Listen on every interface inside the container. config.validate refuses a
# non-loopback bind without a gateway token, so CARTESIA_GW_TOKEN is required
# at runtime; compose reads it from .env.
ENV CARTESIA_GW_ADDR=0.0.0.0:20140 \
    CARTESIA_GW_DB=/data/data.sqlite

# Keys and request history live on a volume, so a recreated container keeps
# them. The mount point sits outside the nonroot home to make it obvious.
VOLUME ["/data"]
EXPOSE 20140

# No HEALTHCHECK: distroless ships no shell, curl or wget, and there is no
# readiness subcommand. Probe GET /healthz from outside instead (compose, a
# reverse proxy, or an uptime checker); it is unauthenticated by design.

# Exec form keeps the binary as PID 1 so SIGTERM from `docker stop` reaches the
# graceful shutdown in main.go instead of being swallowed by a shell.
USER nonroot:nonroot
ENTRYPOINT ["/cartesia-gw"]
