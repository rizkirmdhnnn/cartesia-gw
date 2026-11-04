# Build static, ship tiny: the runtime stage has no shell, no package manager
# and no libc to keep in sync.
FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/cartesia-gw ./cmd/cartesia-gw

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/cartesia-gw /cartesia-gw

# The database lives under the nonroot home so the default -db works as-is.
VOLUME ["/home/nonroot/.cartesia-gw"]
EXPOSE 20140

ENTRYPOINT ["/cartesia-gw"]
