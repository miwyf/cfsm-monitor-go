FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY VERSION ./VERSION
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/cfsm-monitor .

FROM alpine:3.23
LABEL org.opencontainers.image.title="CFSM Monitor Go" \
      org.opencontainers.image.description="CFSM node traffic dashboard" \
      org.opencontainers.image.licenses="MIT"
RUN apk add --no-cache ca-certificates && addgroup -g 10001 cfsm && adduser -D -u 10001 -G cfsm cfsm && mkdir /data && chown cfsm:cfsm /data
COPY --from=build /out/cfsm-monitor /usr/local/bin/cfsm-monitor
COPY --chmod=755 docker-entrypoint.sh /usr/local/bin/cfsm-entrypoint
COPY third-party/ /usr/share/licenses/cfsm-monitor-go/
COPY LICENSE /usr/share/licenses/cfsm-monitor-go/LICENSE
USER 10001:10001
ENV DATA_DIR=/data LISTEN_ADDR=:8080 TZ=Asia/Shanghai
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/cfsm-entrypoint"]
