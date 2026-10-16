#!/usr/bin/env bash
# Restarts Puzzle Table from this folder: stops the running server, then starts
# the puzzle-table binary next to this script in the background, detached from
# the shell so it keeps running after you log out.
#
#   unzip -o puzzle-table.zip && ./deploy.sh
#
# Settings come from ./puzzle-table.env when it exists (same format as
# puzzle-table.env.example). Output goes to ./puzzle-table.log and the process
# id to ./puzzle-table.pid.
set -euo pipefail

cd "$(dirname "$0")"
APP=puzzle-table
PIDFILE=$APP.pid
LOG=$APP.log

running_pids() {
	if [[ -f $PIDFILE ]] && kill -0 "$(cat "$PIDFILE")" 2>/dev/null; then
		cat "$PIDFILE"
	else
		pgrep -x "$APP" || true
	fi
}

pids=$(running_pids)
if [[ -n $pids ]]; then
	echo "Stopping $APP (pid $(echo $pids))"
	kill $pids 2>/dev/null || true
	for _ in $(seq 20); do
		pids=$(running_pids)
		[[ -z $pids ]] && break
		sleep 0.5
	done
	if [[ -n $pids ]]; then
		echo "Still running after 10s; forcing it to stop"
		kill -9 $pids 2>/dev/null || true
	fi
fi
rm -f "$PIDFILE"

if [[ -f $APP.env ]]; then
	set -a
	# shellcheck disable=SC1090
	. "./$APP.env"
	set +a
fi

chmod +x "./$APP"
nohup "./$APP" >>"$LOG" 2>&1 </dev/null &
pid=$!
disown "$pid"
echo "$pid" >"$PIDFILE"

sleep 1
if ! kill -0 "$pid" 2>/dev/null; then
	echo "$APP exited right after starting. Last lines of $LOG:"
	tail -n 20 "$LOG"
	rm -f "$PIDFILE"
	exit 1
fi
echo "Started $APP (pid $pid); logging to $(pwd)/$LOG"
