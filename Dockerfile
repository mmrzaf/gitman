# One image for Gitman's web process, worker and Git hooks; the command
# picks which ("web" by default, "worker" for a worker).
#
# Every source the build downloads from is a build argument, so the image
# builds behind registry and module mirrors:
#
#   docker build \
#     --build-arg GO_IMAGE=registry.example.com/library/golang:1.27-alpine \
#     --build-arg RUNTIME_IMAGE=registry.example.com/library/alpine:3.20 \
#     --build-arg GOPROXY=https://goproxy.example.com,direct \
#     --build-arg ALPINE_MIRROR=https://alpine.example.com/alpine \
#     --build-arg VERSION=1.0.0 \
#     -t gitman:1.0.0 .
ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=alpine:3.20

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/gitman ./cmd/gitman

FROM ${RUNTIME_IMAGE}
ARG ALPINE_MIRROR=
# git serves repositories and fetches run checkouts; the docker client is
# how a worker runs steps. No Go toolchain, compiler or shell tooling
# beyond what these need.
RUN if [ -n "$ALPINE_MIRROR" ]; then \
      sed -i "s#https\?://dl-cdn.alpinelinux.org/alpine#${ALPINE_MIRROR}#" /etc/apk/repositories; \
    fi \
 && apk add --no-cache git docker-cli ca-certificates tzdata \
 && addgroup -S -g 1000 gitman \
 && adduser -S -D -u 1000 -G gitman -h /home/gitman gitman
COPY --from=build /out/gitman /usr/local/bin/gitman
USER gitman
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s \
  CMD wget -q -O /dev/null "http://127.0.0.1:${GITMAN_PORT:-8080}/healthz" || exit 1
ENTRYPOINT ["gitman"]
CMD ["web"]
