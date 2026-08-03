FROM golang:1.25-alpine AS builder

WORKDIR /src

# go.sum does not exist until the first dependency lands; the glob tolerates that.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server


FROM alpine:latest

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/server ./server

# Non-root runtime. /app/data holds the SQLite file. It is created and owned
# here so that a fresh named volume mounted over it inherits uid/gid 1000 —
# that is what lets the compose stack deploy with no host-side chown.
RUN addgroup -g 1000 appuser && \
    adduser -D -u 1000 -G appuser appuser && \
    mkdir -p /app/data && \
    chown -R appuser:appuser /app

USER appuser

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/health || exit 1

CMD ["./server"]
