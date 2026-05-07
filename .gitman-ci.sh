#!/usr/bin/env bash
# ╔══════════════════════════════════════════════════════════════════╗
# ║     Gitman Ultimate CI/CD Pipeline – Reference + Production    ║
# ║            (test, build, docker image, artifact collection)    ║
# ╚══════════════════════════════════════════════════════════════════╝
#
# This script is executed by Gitman's built‑in worker after every push or
# manual trigger. It serves as a complete demonstration of all available
# CI features and is the actual pipeline for the Gitman project itself.
#
# Features demonstrated:
#   • Environment variables:  GITMAN_REPO, GITMAN_COMMIT, GITMAN_BRANCH,
#     GITMAN_TAG, GITMAN_EVENT, GITMAN_RUN_ID
#   • Encrypted secrets injected as env vars (masked output only)
#   • Version injection into binary (from tag or commit)
#   • Static analysis (go vet) + race detector + atomic coverage
#   • Statically linked Linux binary (CGO_ENABLED=0)
#   • Docker image build (optionally push)
#   • Artifacts: binary, coverage, build report, versioned release,
#     Docker image tarball
#   • Branch‑aware logic (main branch deployment simulation)
#
# If Docker is available, a container image is built from the
# Dockerfile in the repo root. When a DEPLOY_TOKEN secret is set,
# the image is also pushed to a registry (GitHub Container Registry
# is used here as an example – adjust the registry accordingly).
#
# Expected repository layout:
#   .gitman-ci.sh  → this file
#   Dockerfile     → container build instructions
#   cmd/gitman/    → Go entry point
#
# Output files in artifacts/ are stored permanently and available
# via the Gitman artifact API.

set -euo pipefail

# ──────────────────────────────────────────────────────────────
#  1. Diagnostic header (always visible in run log)
# ──────────────────────────────────────────────────────────────
echo "======================================================================"
echo "  Gitman Ultimate CI Run"
echo "  Run ID     : ${GITMAN_RUN_ID}"
echo "  Repository : ${GITMAN_REPO}"
echo "  Commit     : ${GITMAN_COMMIT}"
echo "  Branch     : ${GITMAN_BRANCH:-none}"
echo "  Tag        : ${GITMAN_TAG:-none}"
echo "  Event      : ${GITMAN_EVENT}"
echo "  Workspace  : ${PWD}"
echo "======================================================================"
echo ""

# ──────────────────────────────────────────────────────────────
#  2. Secure secret demonstration (never reveal actual values)
# ──────────────────────────────────────────────────────────────
# Secrets you configure in the Gitman UI are injected as env vars.
# In a real pipeline you might use DEPLOY_TOKEN, DB_PASSWORD, etc.
if [[ -n "${DEPLOY_TOKEN:-}" ]]; then
  echo "[secure]  DEPLOY_TOKEN is set (length ${#DEPLOY_TOKEN})"
fi
echo ""

# ──────────────────────────────────────────────────────────────
#  3. Determine build version
# ──────────────────────────────────────────────────────────────
if [[ -n "${GITMAN_TAG:-}" ]]; then
  VERSION="${GITMAN_TAG}"
else
  VERSION="dev-${GITMAN_COMMIT:0:8}"
fi
readonly VERSION
echo "Build version: ${VERSION}"
echo ""

# ──────────────────────────────────────────────────────────────
#  4. Install Go dependencies & run static checks
# ──────────────────────────────────────────────────────────────
echo "--- Downloading Go modules ---"
go mod download
echo ""

echo "--- Running go vet (static analysis) ---"
go vet ./...
echo ""

# ──────────────────────────────────────────────────────────────
#  5. Run tests with race detector and atomic coverage
# ──────────────────────────────────────────────────────────────
echo "--- Running tests (race detector + atomic coverage) ---"
go test ./... -count=1 -race -coverprofile=coverage.out -covermode=atomic
echo ""

# ──────────────────────────────────────────────────────────────
#  6. Build statically linked binary
# ──────────────────────────────────────────────────────────────
echo "--- Building gitman binary (Linux, amd64, static) ---"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags "-X main.version=${VERSION}" -o gitman ./cmd/gitman
file gitman
echo ""

# Quick sanity check
if ! ./gitman --help >/dev/null 2>&1; then
  echo "ERROR: Built binary failed --help check!" >&2
  exit 1
fi

# ──────────────────────────────────────────────────────────────
#  7. Generate a detailed build report
# ──────────────────────────────────────────────────────────────
echo "--- Generating build report ---"
{
  echo "Gitman Build Report"
  echo "==================="
  echo "Commit:       ${GITMAN_COMMIT}"
  echo "Branch:       ${GITMAN_BRANCH:-none}"
  echo "Tag:          ${GITMAN_TAG:-none}"
  echo "Event:        ${GITMAN_EVENT}"
  echo "Version:      ${VERSION}"
  echo "Build date:   $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo ""
  echo "Last commit message:"
  git log -1 --format=%B 2>/dev/null || echo "(not available)"
  echo ""
  echo "Go version:"
  go version
  echo ""
  echo "Worker environment (uname):"
  uname -a
} > build-report.txt
echo ""

# ──────────────────────────────────────────────────────────────
#  8. (Optional) Build Docker image
# ──────────────────────────────────────────────────────────────
# This step uses a Dockerfile from the repository root.
# If the Docker binary is not installed, we skip gracefully.
DOCKER_AVAILABLE="no"
if command -v docker &> /dev/null && docker info >/dev/null 2>&1; then
  DOCKER_AVAILABLE="yes"
fi

IMAGE_NAME="${GITMAN_REPO}:${VERSION}"
IMAGE_FILE="gitman-docker-${VERSION}.tar"

if [[ "${DOCKER_AVAILABLE}" == "yes" ]]; then
  echo "--- Building Docker image: ${IMAGE_NAME} ---"
  docker build -t "${IMAGE_NAME}" .
  echo ""

  # Save image as a tarball artifact (portable, no push needed)
  echo "--- Exporting Docker image to ${IMAGE_FILE} ---"
  docker save "${IMAGE_NAME}" -o "${IMAGE_FILE}"
  echo ""

  # Attempt to push if credentials are available
  if [[ -n "${DEPLOY_TOKEN:-}" ]]; then
    # Example: push to GitHub Container Registry (ghcr.io)
    # Adjust the registry URL for your actual setup.
    REGISTRY="ghcr.io"
    REMOTE_IMAGE="${REGISTRY}/${GITMAN_REPO}:${VERSION}"

    echo "--- Pushing Docker image to ${REMOTE_IMAGE} ---"
    docker tag "${IMAGE_NAME}" "${REMOTE_IMAGE}"
    # Authenticate using the DEPLOY_TOKEN secret.
    # For ghcr.io, the token is a personal access token with write:packages scope.
    echo "${DEPLOY_TOKEN}" | docker login "${REGISTRY}" --username "gitman" --password-stdin
    docker push "${REMOTE_IMAGE}"
    # Also tag and push as 'latest' if on main branch
    if [[ "${GITMAN_BRANCH:-}" == "main" ]]; then
      LATEST_IMAGE="${REGISTRY}/${GITMAN_REPO}:latest"
      docker tag "${IMAGE_NAME}" "${LATEST_IMAGE}"
      docker push "${LATEST_IMAGE}"
    fi
    docker logout "${REGISTRY}"
    echo ""
  else
    echo "[deploy]  No DEPLOY_TOKEN secret – skipping image push."
  fi
else
  echo "[docker]   Docker not available – skipping image build."
  IMAGE_FILE=""  # ensure we don't try to collect it
fi
echo ""

# ──────────────────────────────────────────────────────────────
#  9. Collect all artifacts
# ──────────────────────────────────────────────────────────────
echo "--- Collecting artifacts ---"
mkdir -p artifacts

# Binary
cp -v gitman artifacts/

# Coverage report
cp -v coverage.out artifacts/ 2>/dev/null || echo "No coverage.out found"

# Build report
cp -v build-report.txt artifacts/

# Versioned release (when triggered by a tag)
if [[ -n "${GITMAN_TAG:-}" ]]; then
  cp gitman "artifacts/gitman-${GITMAN_TAG}"
  echo "Release artifact: gitman-${GITMAN_TAG}"
fi

# Docker image tarball (if built)
if [[ -n "${IMAGE_FILE:-}" && -f "${IMAGE_FILE}" ]]; then
  cp -v "${IMAGE_FILE}" "artifacts/${IMAGE_FILE}"
fi

echo ""
echo "--- Artifacts in artifacts/ ---"
ls -alh artifacts/
echo ""

# ──────────────────────────────────────────────────────────────
# 10. Branch‑specific operations (main branch)
# ──────────────────────────────────────────────────────────────
if [[ "${GITMAN_BRANCH:-}" == "main" ]]; then
  echo "--- Main branch deployment simulation ---"
  # Here you could trigger a webhook, scp the binary, or anything else.
  # We only demonstrate the capability.
  if [[ -n "${DEPLOY_TOKEN:-}" ]]; then
    echo "[deploy]  DEPLOY_TOKEN present – real deployment would proceed."
  else
    echo "[deploy]  No DEPLOY_TOKEN secret configured – skipping deployment."
  fi
  echo ""
fi

# ──────────────────────────────────────────────────────────────
# 11. Final success message
# ──────────────────────────────────────────────────────────────
echo "======================================================================"
echo "  CI run ${GITMAN_RUN_ID} completed successfully"
echo "======================================================================"
exit 0
