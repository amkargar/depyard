FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/mirror-registry ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /out/mirror-registry /usr/local/bin/mirror-registry
ENV LISTEN_ADDR=0.0.0.0:8080
ENV DATA_DIR=/data
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/mirror-registry"]
