## Build stage
FROM golang:1.24-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /shelly-exporter .

## Runtime stage
FROM scratch

COPY --from=builder /shelly-exporter /shelly-exporter
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Default config path (mount your config.json here via a volume)
ENV CONFIG_PATH=/etc/shelly-exporter/config.json

ENTRYPOINT ["/shelly-exporter"]
