# Q8 Relay — built for amd64 and arm64:
#   docker buildx build --platform linux/amd64,linux/arm64 -t ghcr.io/q8-app/q8-relay .
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/q8-relay ./cmd/q8-relay

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/q8-relay /q8-relay
ENV RELAY_DATA=/config RELAY_PORT=8484
EXPOSE 8484
VOLUME /config
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/q8-relay", "-healthcheck"]
ENTRYPOINT ["/q8-relay"]
