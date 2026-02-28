#!/usr/bin/env bash
#
# Ulanzi D200 Installer — downloads ulanzi-control from GitHub Releases
# and deploys it to a D200 connected via ADB.
#
# Usage:
#   ./install.sh              # install latest release
#   ./install.sh v0.2.0       # install specific version
#
set -euo pipefail

REPO="mapero/esphome-ulanzi-d200"
INSTALL_DIR="/userdata/ulanzi-control"
BINARY_NAME="ulanzi-control"
INIT_SCRIPT="/etc/init.d/S95ulanzi"

# ── Colours ──────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
    RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'
    BLUE='\033[0;34m'; BOLD='\033[1m'; NC='\033[0m'
else
    RED=''; GREEN=''; YELLOW=''; BLUE=''; BOLD=''; NC=''
fi

info()  { printf "${BLUE}[INFO]${NC}  %s\n" "$*"; }
ok()    { printf "${GREEN}[OK]${NC}    %s\n" "$*"; }
warn()  { printf "${YELLOW}[WARN]${NC}  %s\n" "$*"; }
error() { printf "${RED}[ERROR]${NC} %s\n" "$*" >&2; exit 1; }

# ── Temp dir cleanup ─────────────────────────────────────────────────────────
TMPDIR_INST=""
cleanup() { [ -n "$TMPDIR_INST" ] && rm -rf "$TMPDIR_INST"; }
trap cleanup EXIT
TMPDIR_INST=$(mktemp -d)

# ── Check ADB ────────────────────────────────────────────────────────────────
info "Checking for ADB..."
command -v adb >/dev/null 2>&1 || error "adb not found. Please install Android SDK Platform Tools."

DEVICE_INFO=$(adb devices -l 2>/dev/null | grep -v "^List" | grep -v "^$" | head -1) || true
[ -z "$DEVICE_INFO" ] && error "No ADB device connected. Connect your D200 via USB and enable ADB."
ok "Device found: $DEVICE_INFO"

# ── Resolve version ──────────────────────────────────────────────────────────
VERSION="${1:-}"

fetch_url() {
    local url="$1" output="$2"
    if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$output"
    elif command -v wget >/dev/null 2>&1; then
        wget -qO "$output" "$url"
    else
        error "Neither curl nor wget found. Please install one of them."
    fi
}

if [ -z "$VERSION" ]; then
    info "Fetching latest release..."
    API_URL="https://api.github.com/repos/${REPO}/releases/latest"
else
    info "Fetching release ${VERSION}..."
    API_URL="https://api.github.com/repos/${REPO}/releases/tags/${VERSION}"
fi

RELEASE_JSON="$TMPDIR_INST/release.json"
fetch_url "$API_URL" "$RELEASE_JSON" || error "Failed to fetch release info from GitHub API."

# Parse tag_name from JSON (no jq dependency)
TAG=$(grep -o '"tag_name"[[:space:]]*:[[:space:]]*"[^"]*"' "$RELEASE_JSON" | head -1 | sed 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/')
[ -z "$TAG" ] && error "Could not determine release version. Check that the version exists."

ok "Version: $TAG"

# ── Download binary ──────────────────────────────────────────────────────────
DOWNLOAD_URL="https://github.com/${REPO}/releases/download/${TAG}/${BINARY_NAME}"
BINARY_PATH="$TMPDIR_INST/$BINARY_NAME"

info "Downloading ${BINARY_NAME} (${TAG})..."
fetch_url "$DOWNLOAD_URL" "$BINARY_PATH" || error "Failed to download binary from ${DOWNLOAD_URL}"
ok "Downloaded $(wc -c < "$BINARY_PATH" | tr -d ' ') bytes"

# ── Check for existing installation ──────────────────────────────────────────
EXISTING=""
if adb shell "[ -x ${INSTALL_DIR}/${BINARY_NAME} ]" 2>/dev/null; then
    EXISTING="yes"
    warn "Existing installation detected — upgrading"

    info "Stopping service..."
    adb shell "[ -x ${INIT_SCRIPT} ] && ${INIT_SCRIPT} stop" 2>/dev/null || true
    sleep 2  # wait for serial port release
    ok "Service stopped"
fi

# ── Deploy to device ─────────────────────────────────────────────────────────
info "Creating install directory..."
adb shell "mkdir -p ${INSTALL_DIR}" || error "Failed to create ${INSTALL_DIR} on device"

info "Pushing binary..."
adb push "$BINARY_PATH" "${INSTALL_DIR}/${BINARY_NAME}" || error "Failed to push binary"
adb shell "chmod 755 ${INSTALL_DIR}/${BINARY_NAME}"
ok "Binary deployed"

# ── Write embedded scripts ───────────────────────────────────────────────────
WRAPPER_PATH="$TMPDIR_INST/ulanzi-run.sh"
cat > "$WRAPPER_PATH" << 'WRAPPER_EOF'
#!/bin/sh
#
# Wrapper for ulanzi-control that captures stdout+stderr to a log file,
# rotates old logs on startup, and restarts the daemon on crash.

DAEMON=/userdata/ulanzi-control/ulanzi-control
LOGDIR=/userdata/ulanzi-control/logs
LOGFILE=$LOGDIR/ulanzi-control.log
MAX_LOGS=5       # keep this many rotated logs
RESTART_DELAY=3  # seconds to wait before restarting after a crash

mkdir -p "$LOGDIR"

# Rotate logs: ulanzi-control.log -> .1 -> .2 ... -> .MAX_LOGS (oldest dropped)
i=$MAX_LOGS
while [ $i -gt 1 ]; do
	prev=$((i - 1))
	[ -f "$LOGDIR/ulanzi-control.$prev.log" ] && \
		mv "$LOGDIR/ulanzi-control.$prev.log" "$LOGDIR/ulanzi-control.$i.log"
	i=$prev
done
[ -f "$LOGFILE" ] && mv "$LOGFILE" "$LOGDIR/ulanzi-control.1.log"

# Run loop: restart daemon on crash
while true; do
	echo "$(date '+%Y-%m-%d %H:%M:%S') [wrapper] Starting $DAEMON" >> "$LOGFILE"
	cd /userdata/ulanzi-control
	"$DAEMON" >> "$LOGFILE" 2>&1
	EXIT_CODE=$?
	echo "$(date '+%Y-%m-%d %H:%M:%S') [wrapper] Exited with code $EXIT_CODE" >> "$LOGFILE"

	# Exit code 0 means intentional stop (SIGTERM from stop script)
	[ $EXIT_CODE -eq 0 ] && break

	echo "$(date '+%Y-%m-%d %H:%M:%S') [wrapper] Restarting in ${RESTART_DELAY}s..." >> "$LOGFILE"
	sleep $RESTART_DELAY
done
WRAPPER_EOF

INIT_PATH="$TMPDIR_INST/S95ulanzi"
cat > "$INIT_PATH" << 'INIT_EOF'
#!/bin/sh

DAEMON=/userdata/ulanzi-control/ulanzi-control
WRAPPER=/userdata/ulanzi-control/ulanzi-run.sh
PIDFILE=/var/run/ulanzi-control.pid

[ -x "$DAEMON" ] || exit 0
[ -x "$WRAPPER" ] || exit 0

start() {
	printf "Starting ulanzi-control: "
	start-stop-daemon -S -b -m -p "$PIDFILE" -x "$WRAPPER"
	echo "done"
}

stop() {
	printf "Stopping ulanzi-control: "
	start-stop-daemon -K -p "$PIDFILE" -x "$WRAPPER"
	# Also kill the daemon itself in case it outlives the wrapper
	killall ulanzi-control 2>/dev/null || true
	rm -f "$PIDFILE"
	echo "done"
}

case "$1" in
  start)
	start
	;;
  stop)
	stop
	;;
  restart|reload)
	stop
	sleep 1
	start
	;;
  *)
	echo "Usage: $0 {start|stop|restart}"
	exit 1
esac

exit $?
INIT_EOF

info "Pushing wrapper script..."
adb push "$WRAPPER_PATH" "${INSTALL_DIR}/ulanzi-run.sh" || error "Failed to push wrapper script"
adb shell "chmod 755 ${INSTALL_DIR}/ulanzi-run.sh"
ok "Wrapper script deployed"

info "Pushing init script..."
adb push "$INIT_PATH" "${INIT_SCRIPT}" || error "Failed to push init script"
adb shell "chmod 755 ${INIT_SCRIPT}"
ok "Init script deployed"

# ── Start service ────────────────────────────────────────────────────────────
info "Starting service..."
adb shell "${INIT_SCRIPT} start" || error "Failed to start service"
sleep 1

# Verify process is running
if adb shell "pidof ${BINARY_NAME}" >/dev/null 2>&1; then
    PID=$(adb shell "pidof ${BINARY_NAME}" | tr -d '\r')
    ok "Service running (PID: ${PID})"
else
    warn "Process not found — check logs with: adb shell 'cat ${INSTALL_DIR}/logs/ulanzi-control.log'"
fi

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
printf "${BOLD}════════════════════════════════════════════════════${NC}\n"
if [ -n "$EXISTING" ]; then
    printf "${GREEN}${BOLD}  Upgrade complete!${NC}  ${TAG}\n"
else
    printf "${GREEN}${BOLD}  Installation complete!${NC}  ${TAG}\n"
fi
printf "${BOLD}════════════════════════════════════════════════════${NC}\n"
echo ""
echo "Useful commands:"
echo "  View logs:       adb shell 'cat ${INSTALL_DIR}/logs/ulanzi-control.log'"
echo "  Follow logs:     adb shell 'tail -f ${INSTALL_DIR}/logs/ulanzi-control.log'"
echo "  Restart service: adb shell '${INIT_SCRIPT} restart'"
echo "  Stop service:    adb shell '${INIT_SCRIPT} stop'"
echo ""
