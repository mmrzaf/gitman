# One image for Gitman's web process, worker and Git hooks:
#
#   docker run gitman:1.0.0 gitman web
#   docker run --user root -v /var/run/docker.sock:/var/run/docker.sock \
#     -v /srv/gitman:/srv/gitman gitman:1.0.0 gitman worker
#
# Base images, the Alpine package mirror and the Go module proxy are build
# arguments so the image can build behind mirrors:
#
#   docker build \
#     --build-arg GO_IMAGE=registry.example.com/library/golang:1.27-alpine \
#     --build-arg RUNTIME_IMAGE=registry.example.com/library/alpine:3.24 \
#     --build-arg DOCKER_CLI_IMAGE=registry.example.com/library/docker:29-cli \
#     --build-arg APK_MIRROR=https://alpine.example.com/alpine \
#     --build-arg GOPROXY=https://goproxy.example.com,direct \
#     --build-arg VERSION=v1.0.0 \
#     -t gitman:1.0.0 .
ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=alpine:3.24
# The worker's docker client. Taken from the Docker project's own image,
# whose client is a static binary that runs on any base.
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
ARG APK_MIRROR=https://dl-cdn.alpinelinux.org/alpine
# git serves repositories and fetches run checkouts; curl is the health
# check; the docker client is how a worker runs steps. The package
# repositories follow the base image's release, so a mirror needs the same
# /vMAJOR.MINOR/{main,community} layout as Alpine's own.
RUN set -eu; \
    release="v$(cut -d. -f1,2 /etc/alpine-release)"; \
    printf '%s/%s/main\n%s/%s/community\n' "$APK_MIRROR" "$release" "$APK_MIRROR" "$release" > /etc/apk/repositories; \
    apk add --no-cache git ca-certificates curl tzdata; \
    addgroup -g 1000 gitman; \
    adduser -D -u 1000 -G gitman -h /home/gitman -s /sbin/nologin gitman
COPY --from=docker-cli /usr/local/bin/docker /usr/local/bin/docker
COPY --from=build /out/gitman /usr/local/bin/gitman
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/gitman/
USER gitman
EXPOSE 8080
# Health checks belong to the deployment, not the image: the same image
# also runs the worker, which has no HTTP port. compose.yaml checks web.
CMD ["gitman", "web"]
