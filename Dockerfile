# agendarr — multi-arch image (linux/amd64, linux/arm64, linux/arm/v7).
# CGO_ENABLED=0 everywhere (pure-Go SQLite), so cross-compilation is native and
# no target-architecture code runs during the build.

# Digest-pinned: the tag alone follows Alpine releases. Dependabot bumps both.
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X github.com/neotoxicfr/agendarr/internal/server.Version=${VERSION}" \
      -o /out/agendarr ./cmd/agendarr \
    && mkdir -p /out/data

# distroless/static rather than scratch: agendarr calls plex.tv over HTTPS and
# needs a CA bundle, and time zones for the calendar. Still no shell, no
# package manager, and a non-root user.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/agendarr /agendarr
# /data belongs to nonroot so freshly created named volumes are writable.
COPY --from=build --chown=nonroot:nonroot /out/data /data
ENV AGENDARR_LISTEN=:8080 \
    AGENDARR_DATA_DIR=/data
VOLUME /data
EXPOSE 8080
USER nonroot
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s CMD ["/agendarr", "healthcheck"]
ENTRYPOINT ["/agendarr"]
