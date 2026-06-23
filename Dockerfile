ARG GO_IMAGE=golang:1.26-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS builder

ARG DEBIAN_MIRROR=http://linux-mirror.liara.ir/repository/debian
ARG DEBIAN_SECURITY_MIRROR=http://linux-mirror.liara.ir/repository/debian-security
ARG GOPROXY=https://mirror.abrha.net/repository/go/,direct
ENV GOPROXY=${GOPROXY} \
	GOTOOLCHAIN=local

RUN set -eu; \
	rm -f /etc/apt/sources.list.d/debian.sources; \
	printf 'deb %s bookworm main\ndeb %s bookworm-updates main\ndeb %s bookworm-security main\n' \
		"$DEBIAN_MIRROR" "$DEBIAN_MIRROR" "$DEBIAN_SECURITY_MIRROR" > /etc/apt/sources.list; \
	apt-get update; \
	apt-get install -y --no-install-recommends git ca-certificates; \
	rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /gitman ./cmd/gitman

FROM ${RUNTIME_IMAGE}

ARG DEBIAN_MIRROR=http://linux-mirror.liara.ir/repository/debian
ARG DEBIAN_SECURITY_MIRROR=http://linux-mirror.liara.ir/repository/debian-security
ARG GIT_UID=1000

RUN set -eu; \
	rm -f /etc/apt/sources.list.d/debian.sources; \
	printf 'deb %s bookworm main\ndeb %s bookworm-updates main\ndeb %s bookworm-security main\n' \
		"$DEBIAN_MIRROR" "$DEBIAN_MIRROR" "$DEBIAN_SECURITY_MIRROR" > /etc/apt/sources.list; \
	apt-get update; \
	apt-get install -y --no-install-recommends git curl bash docker.io ca-certificates; \
	rm -rf /var/lib/apt/lists/*; \
	useradd --create-home --home-dir /data --uid "${GIT_UID}" --shell /bin/bash git; \
	mkdir -p /data; \
	chown -R git:git /data

COPY --from=builder /gitman /usr/local/bin/gitman

USER git
WORKDIR /data

CMD ["gitman", "web"]
