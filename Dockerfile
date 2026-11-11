FROM --platform=$BUILDPLATFORM golang:1.27.3-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
COPY cmd ./cmd
COPY geo ./geo
COPY routeros ./routeros
COPY docs/home-assistant-observatory.yaml ./docs/
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/ros-watch . \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/probe ./cmd/probe \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/entrypoint ./cmd/entrypoint \
    && mkdir -p /out/data && chown 1000:1000 /out/data

FROM build AS test
RUN go test -count=1 ./... && go vet ./...
ARG ALPINE_MIRROR=https://dl-cdn.alpinelinux.org
RUN sed -i "s|https://dl-cdn.alpinelinux.org|${ALPINE_MIRROR}|g" /etc/apk/repositories \
    && apk add --no-cache gcc musl-dev && go test -race -count=1 ./...

FROM scratch AS artifacts
COPY --from=build /out/probe /probe

FROM scratch AS production
ENV DATA_DIR=/data GOMEMLIMIT=128MiB TMPDIR=/data SQLITE_TMPDIR=/data
COPY --from=build /out/ros-watch /ros-watch
COPY --from=build /out/entrypoint /entrypoint
COPY --from=build /src/geo /geo
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build --chown=1000:1000 /out/data /data
USER 0:0
VOLUME ["/data"]
EXPOSE 8080/tcp 5514/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/ros-watch", "healthcheck"]
ENTRYPOINT ["/entrypoint"]
