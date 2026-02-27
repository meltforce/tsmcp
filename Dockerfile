FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /tsmcp .

FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 1000 mcp-bridge

ARG VERSION=dev
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.source="https://github.com/meltforce/tsmcp"
LABEL org.opencontainers.image.description="MCP Tailnet Bridge"

COPY --from=builder /tsmcp /usr/local/bin/tsmcp

USER mcp-bridge

HEALTHCHECK --interval=10s --timeout=3s --start-period=30s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8900/healthz || exit 1

ENTRYPOINT ["tsmcp"]
CMD ["-config", "/etc/mcp-bridge/config.yaml"]
