# ---- Build Stage ----
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /gitman ./cmd/gitman

# ---- Runtime Stage ----
FROM alpine:3

RUN apk add --no-cache git

RUN adduser -D -h /data git

COPY --from=builder /gitman /usr/local/bin/gitman

WORKDIR /data
RUN mkdir -p /data && chown gitn:git /data

USER git

CMD ["gitman", "web"]
