FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
COPY cmd ./cmd
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ros-watch . \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/probe ./cmd/probe \
    && mkdir -p /out/data && chown 10001:10001 /out/data

FROM build AS test
RUN go test -count=1 ./... && go vet ./...
RUN apk add --no-cache gcc musl-dev && go test -race -count=1 ./...

FROM scratch AS artifacts
COPY --from=build /out/probe /probe

FROM scratch AS production
ENV DATA_DIR=/data GOMEMLIMIT=128MiB
COPY --from=build /out/ros-watch /ros-watch
COPY --from=build --chown=10001:10001 /out/data /data
USER 10001:10001
VOLUME ["/data"]
EXPOSE 8080/tcp 5514/udp
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD ["/ros-watch", "healthcheck"]
ENTRYPOINT ["/ros-watch"]
