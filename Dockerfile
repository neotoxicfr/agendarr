# agendarr — multi-arch image (linux/amd64, linux/arm64, linux/arm/v7).
# CGO_ENABLED=0 everywhere (pure-Go SQLite), so cross-compilation is native and
# no target-architecture code runs during the build.

# Digest-pinned: the tag alone follows Alpine releases. Dependabot bumps both.
FROM --platform=$BUILDPLATFORM golang:1.27.2-alpine@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
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
FROM gcr.io/distroless/static-debian13:nonroot@sha256:516757106de35205efb70b91ba03f6f1298424a0678c603255cf2852a7000087
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
