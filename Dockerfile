# One image for Gitman's web process, worker and Git hooks:
#
#   docker run gitman:1.0.0 gitman web
#   docker run gitman:1.0.0 gitman worker
#
# Base images, Debian mirrors and the Go module proxy are build arguments
# so the image can build behind mirrors:
#
#   docker build \
#     --build-arg GO_IMAGE=registry.example.com/library/golang:1.27-bookworm \
#     --build-arg RUNTIME_IMAGE=registry.example.com/library/debian:bookworm-slim \
#     --build-arg DOCKER_CLI_IMAGE=registry.example.com/library/docker:29-cli \
#     --build-arg DEBIAN_MIRROR=http://debian.example.com/debian \
#     --build-arg DEBIAN_SECURITY_MIRROR=http://debian.example.com/debian-security \
#     --build-arg GOPROXY=https://goproxy.example.com,direct \
#     --build-arg VERSION=v1.0.0 \
#     -t gitman:1.0.0 .
ARG GO_IMAGE=golang:1.27-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim
# The worker's docker client. Taken from the Docker project's own image,
# whose client is a static binary: Debian's docker.io is too old for the
# API versions current Docker engines accept.
ARG DOCKER_CLI_IMAGE=docker:29-cli

FROM ${DOCKER_CLI_IMAGE} AS docker-cli

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
ENV GOPROXY=${GOPROXY} \
    GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/gitman ./cmd/gitman

FROM ${RUNTIME_IMAGE}
ARG DEBIAN_MIRROR=http://deb.debian.org/debian
ARG DEBIAN_SECURITY_MIRROR=http://security.debian.org/debian-security
# git serves repositories and fetches run checkouts; curl is the health
# check; the docker client is how a worker runs steps.
RUN set -eu; \
    rm -f /etc/apt/sources.list.d/debian.sources; \
    printf 'deb %s bookworm main\ndeb %s bookworm-updates main\ndeb %s bookworm-security main\n' \
      "$DEBIAN_MIRROR" "$DEBIAN_MIRROR" "$DEBIAN_SECURITY_MIRROR" > /etc/apt/sources.list; \
    apt-get update; \
    apt-get install -y --no-install-recommends git ca-certificates curl tzdata; \
    rm -rf /var/lib/apt/lists/*; \
    useradd --uid 1000 --user-group --create-home --home-dir /home/gitman --shell /usr/sbin/nologin gitman
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/gitman /usr/local/bin/gitman
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/gitman/
USER gitman
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
  CMD curl -fsS -o /dev/null "http://127.0.0.1:${GITMAN_PORT:-8080}/readyz" || exit 1
CMD ["gitman", "web"]
