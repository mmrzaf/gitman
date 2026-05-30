FROM golang:1.24.6-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN GOTOOLCHAIN=local go mod download

COPY . .
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -ldflags="-s -w" -o /gitman ./cmd/gitman

FROM alpine:3.20

RUN apk add --no-cache git git-daemon curl bash docker-cli ca-certificates

RUN addgroup -g 999 docker 2>/dev/null || true \
	&& adduser -D -h /data -u 1000 git \
	&& adduser git docker

COPY --from=builder /gitman /usr/local/bin/gitman

RUN mkdir -p /data \
	&& chown -R git:git /data

USER git
WORKDIR /data

CMD ["gitman", "web"]

