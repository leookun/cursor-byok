#!/bin/sh
set -eu

log() {
    printf '[linux-build] %s\n' "$*"
}

export GOOS=linux
export GOARCH=amd64
export CGO_ENABLED=1
export CGO_CFLAGS="-w"

APP=${APP_NAME:-$(basename "$(pwd)")}
BIN_PATH="bin/${APP}-linux-amd64"
LOG_MODE=${GO_BUILD_LOG_MODE:-quiet}

case "$LOG_MODE" in
    quiet|heartbeat)
        ;;
    verbose|trace)
        log "workspace: $(pwd)"
        log "target: ${APP} (${GOOS}/${GOARCH})"
        log "go version: $(go version)"
        log "log mode: ${LOG_MODE}"
        ;;
    *)
        log "unknown GO_BUILD_LOG_MODE=${LOG_MODE}, falling back to quiet"
        LOG_MODE="quiet"
        ;;
esac

if [ -d "frontend" ] && [ -f "frontend/package.json" ] && [ ! -d "frontend/dist" ]; then
    log "frontend/dist missing, building frontend assets in container"
    (
        cd frontend
        yarn install --frozen-lockfile
        yarn run build
    )
fi

mkdir -p bin

LDFLAGS="-s -w"
if [ -n "${EXTRA_LDFLAGS:-}" ]; then
    LDFLAGS="$LDFLAGS $EXTRA_LDFLAGS"
fi

TAGS="production"
if [ -n "${EXTRA_TAGS:-}" ]; then
    TAGS="${TAGS},${EXTRA_TAGS}"
fi

set -- go build
case "$LOG_MODE" in
    quiet)
        ;;
    heartbeat)
        ;;
    verbose)
        set -- "$@" -v
        ;;
    trace)
        set -- "$@" -x -v
        ;;
esac

set -- "$@" -tags "$TAGS" -trimpath -buildvcs=false -ldflags "$LDFLAGS" -o "$BIN_PATH" .

if "$@"; then
    :
else
    status="$?"
    log "go build failed with exit code ${status}"
    exit "$status"
fi

case "$LOG_MODE" in
    verbose|trace)
        log "built: ${BIN_PATH}"
        ;;
esac
