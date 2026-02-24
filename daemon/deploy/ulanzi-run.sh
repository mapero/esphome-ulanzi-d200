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
