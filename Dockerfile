FROM golang:1.26.4-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src
COPY go.mod go.sum ./
RUN GOTOOLCHAIN=local go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 GOTOOLCHAIN=local go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /gitman ./cmd/gitman

FROM alpine:3.24

ARG GIT_UID=1000
RUN apk add --no-cache git git-daemon curl bash docker-cli ca-certificates \
	&& adduser -D -h /data -u "${GIT_UID}" git \
	&& mkdir -p /data \
	&& chown -R git:git /data

COPY --from=builder /gitman /usr/local/bin/gitman

USER git
WORKDIR /data

CMD ["gitman", "web"]
