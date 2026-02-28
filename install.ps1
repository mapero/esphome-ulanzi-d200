#Requires -Version 5.1
<#
.SYNOPSIS
    Ulanzi D200 Installer — downloads ulanzi-control from GitHub Releases
    and deploys it to a D200 connected via ADB.

.EXAMPLE
    powershell -ExecutionPolicy Bypass -File install.ps1
    powershell -ExecutionPolicy Bypass -File install.ps1 -Version v0.2.0
#>
param(
    [string]$Version = ""
)

$ErrorActionPreference = "Stop"

$Repo = "mapero/esphome-ulanzi-d200"
$InstallDir = "/userdata/ulanzi-control"
$BinaryName = "ulanzi-control"
$InitScript = "/etc/init.d/S95ulanzi"

# ── Helpers ──────────────────────────────────────────────────────────────────

function Write-Info  { param([string]$Msg) Write-Host "[INFO]  $Msg" -ForegroundColor Cyan }
function Write-Ok    { param([string]$Msg) Write-Host "[OK]    $Msg" -ForegroundColor Green }
function Write-Warn  { param([string]$Msg) Write-Host "[WARN]  $Msg" -ForegroundColor Yellow }
function Write-Err   { param([string]$Msg) Write-Host "[ERROR] $Msg" -ForegroundColor Red; exit 1 }

# ── Temp dir ─────────────────────────────────────────────────────────────────
$TmpDir = Join-Path ([System.IO.Path]::GetTempPath()) "ulanzi-install-$([System.Guid]::NewGuid().ToString('N').Substring(0,8))"
New-Item -ItemType Directory -Path $TmpDir -Force | Out-Null

try {

# ── Check ADB ────────────────────────────────────────────────────────────────
Write-Info "Checking for ADB..."
$adbPath = Get-Command adb -ErrorAction SilentlyContinue
if (-not $adbPath) {
    Write-Err "adb not found. Please install Android SDK Platform Tools and add to PATH."
}

$devices = & adb devices -l 2>&1
$deviceLine = ($devices -split "`n" | Where-Object { $_ -match "\S" -and $_ -notmatch "^List" }) | Select-Object -First 1
if (-not $deviceLine -or $deviceLine.Trim() -eq "") {
    Write-Err "No ADB device connected. Connect your D200 via USB and enable ADB."
}
Write-Ok "Device found: $($deviceLine.Trim())"

# ── Resolve version ──────────────────────────────────────────────────────────
if ($Version -eq "") {
    Write-Info "Fetching latest release..."
    $apiUrl = "https://api.github.com/repos/$Repo/releases/latest"
} else {
    Write-Info "Fetching release $Version..."
    $apiUrl = "https://api.github.com/repos/$Repo/releases/tags/$Version"
}

try {
    $release = Invoke-RestMethod -Uri $apiUrl -UseBasicParsing
} catch {
    Write-Err "Failed to fetch release info from GitHub API. $_"
}

$Tag = $release.tag_name
if (-not $Tag) {
    Write-Err "Could not determine release version. Check that the version exists."
}
Write-Ok "Version: $Tag"

# ── Download binary ──────────────────────────────────────────────────────────
$downloadUrl = "https://github.com/$Repo/releases/download/$Tag/$BinaryName"
$binaryPath = Join-Path $TmpDir $BinaryName

Write-Info "Downloading $BinaryName ($Tag)..."
try {
    Invoke-WebRequest -Uri $downloadUrl -OutFile $binaryPath -UseBasicParsing
} catch {
    Write-Err "Failed to download binary from $downloadUrl. $_"
}
$size = (Get-Item $binaryPath).Length
Write-Ok "Downloaded $size bytes"

# ── Check for existing installation ──────────────────────────────────────────
$existing = $false
$checkResult = & adb shell "[ -x $InstallDir/$BinaryName ] && echo YES" 2>&1
if ($checkResult -match "YES") {
    $existing = $true
    Write-Warn "Existing installation detected - upgrading"

    Write-Info "Stopping service..."
    & adb shell "[ -x $InitScript ] && $InitScript stop" 2>&1 | Out-Null
    Start-Sleep -Seconds 2
    Write-Ok "Service stopped"
}

# ── Deploy to device ─────────────────────────────────────────────────────────
Write-Info "Creating install directory..."
& adb shell "mkdir -p $InstallDir"
if ($LASTEXITCODE -ne 0) { Write-Err "Failed to create $InstallDir on device" }

Write-Info "Pushing binary..."
& adb push $binaryPath "$InstallDir/$BinaryName"
if ($LASTEXITCODE -ne 0) { Write-Err "Failed to push binary" }
& adb shell "chmod 755 $InstallDir/$BinaryName"
Write-Ok "Binary deployed"

# ── Write embedded scripts (LF line endings) ────────────────────────────────
$wrapperContent = @'
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
'@

$initContent = @'
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
'@

$wrapperPath = Join-Path $TmpDir "ulanzi-run.sh"
$initPath = Join-Path $TmpDir "S95ulanzi"

# Write with LF line endings (device runs Linux)
[System.IO.File]::WriteAllText($wrapperPath, $wrapperContent.Replace("`r`n", "`n") + "`n")
[System.IO.File]::WriteAllText($initPath, $initContent.Replace("`r`n", "`n") + "`n")

Write-Info "Pushing wrapper script..."
& adb push $wrapperPath "$InstallDir/ulanzi-run.sh"
if ($LASTEXITCODE -ne 0) { Write-Err "Failed to push wrapper script" }
& adb shell "chmod 755 $InstallDir/ulanzi-run.sh"
Write-Ok "Wrapper script deployed"

Write-Info "Pushing init script..."
& adb push $initPath $InitScript
if ($LASTEXITCODE -ne 0) { Write-Err "Failed to push init script" }
& adb shell "chmod 755 $InitScript"
Write-Ok "Init script deployed"

# ── Start service ────────────────────────────────────────────────────────────
Write-Info "Starting service..."
& adb shell "$InitScript start"
if ($LASTEXITCODE -ne 0) { Write-Err "Failed to start service" }
Start-Sleep -Seconds 1

$processPid = (& adb shell "pidof $BinaryName" 2>&1).Trim()
if ($processPid) {
    Write-Ok "Service running (PID: $processPid)"
} else {
    Write-Warn "Process not found - check logs with: adb shell 'cat $InstallDir/logs/ulanzi-control.log'"
}

# ── Summary ──────────────────────────────────────────────────────────────────
Write-Host ""
Write-Host ("=" * 52) -ForegroundColor White
if ($existing) {
    Write-Host "  Upgrade complete!  $Tag" -ForegroundColor Green
} else {
    Write-Host "  Installation complete!  $Tag" -ForegroundColor Green
}
Write-Host ("=" * 52) -ForegroundColor White
Write-Host ""
Write-Host "Useful commands:"
Write-Host "  View logs:       adb shell 'cat $InstallDir/logs/ulanzi-control.log'"
Write-Host "  Follow logs:     adb shell 'tail -f $InstallDir/logs/ulanzi-control.log'"
Write-Host "  Restart service: adb shell '$InitScript restart'"
Write-Host "  Stop service:    adb shell '$InitScript stop'"
Write-Host ""

} finally {
    # ── Cleanup ──────────────────────────────────────────────────────────────
    if (Test-Path $TmpDir) {
        Remove-Item -Recurse -Force $TmpDir
    }
}
