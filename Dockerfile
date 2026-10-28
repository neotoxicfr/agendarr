# agendarr — multi-arch image (linux/amd64, linux/arm64, linux/arm/v7).
# CGO_ENABLED=0 everywhere (pure-Go SQLite), so cross-compilation is native and
# no target-architecture code runs during the build.

# Digest-pinned: the tag alone follows Alpine releases. Dependabot bumps both.
FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build
ARG TARGETOS TARGETARCH VERSION=dev REVISION=
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY web/ ./web/
# The version stamps asset URLs (?v=) and the service worker caches: a :dev
# build must change it on every commit, or browsers keep last week's CSS.
RUN V="$VERSION"; [ "$V" = dev ] && [ -n "$REVISION" ] && V="dev-$(echo "$REVISION" | cut -c1-7)"; \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
      -ldflags="-s -w -X github.com/neotoxicfr/agendarr/internal/server.Version=${V}" \
      -o /out/agendarr ./cmd/agendarr \
    && mkdir -p /out/data

# distroless/static rather than scratch: agendarr calls plex.tv over HTTPS and
# needs a CA bundle, and time zones for the calendar. Still no shell, no
# package manager, and a non-root user.
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
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
