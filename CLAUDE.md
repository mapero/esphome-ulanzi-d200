# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

ESPHome external component for the **Ulanzi D200** stream deck. An ESP32 bridges the D200 display to Home Assistant over WiFi. The D200 has a 14-button capacitive grid (5x3 layout, button 13 is double-wide).

Two main subsystems:
- **ESP32 component** (`components/ulanzi_d200/`) — ESPHome Python schema + C++ bridge logic
- **Go daemon** (`daemon/`) — runs on the D200 itself (RK3308, ARMv7 Linux), renders UI and handles input

Communication: NDJSON over UART at 1.5 Mbps.

## Build Commands

### ESP32 Component (ESPHome)

```bash
# Compile firmware (from repo root or examples dir)
esphome compile examples/basic.yaml

# Flash to device
esphome run examples/basic.yaml

# View logs
esphome logs examples/basic.yaml
```

Requires `secrets.yaml` in `examples/` — copy from `examples/secrets.yaml.example`.

### Go Daemon

```bash
# Build for D200 (ARMv7 Linux)
cd daemon && GOOS=linux GOARCH=arm GOARM=7 go build -o ulanzi-control ./cmd/ulanzi-control/

# Build for local development
cd daemon && go build -o ulanzi-control ./cmd/ulanzi-control/

# Deploy to D200 via ADB
adb push daemon/ulanzi-control /userdata/ulanzi-control/
adb push daemon/deploy/S95ulanzi /userdata/ulanzi-control/
adb push daemon/deploy/ulanzi-run.sh /userdata/ulanzi-control/

# /etc/init.d is read-only; create a symlink instead
adb shell ln -sf /userdata/ulanzi-control/S95ulanzi /etc/init.d/S95ulanzi
```

Go 1.22+ required. Dependencies: `golang.org/x/image`, `go.bug.st/serial`.

## Architecture

### Concept Model

```
ulanzi_d200 (1 per device)
  └── sections[] (physical button positions 0–13)
        ├── position, page (-1 = all pages, 0+ = specific page)
        ├── on_press / on_release (ESPHome automations)
        └── layouts[] (conditional display states, first match wins)
              ├── when (lambda condition, evaluated every 1s)
              ├── widget type (entity_view | gauge | line_graph | status | chips | notification)
              └── properties (text, color, icon_id, etc. — static or lambda)
```

### ESP32 Component (C++/Python)

- `__init__.py` — ESPHome YAML schema validation and C++ code generation. Defines all widget types and their properties. Processes lambda vs static string properties.
- `ulanzi_d200.h` — `UlanziBridge` class (extends `Component`, `UARTDevice`). Also declares `UlanziBacklightLight` (brightness-only light entity), trigger classes, and data structures for sections/layouts/widgets.
- `ulanzi_d200.cpp` — Core implementation:
  - **Layout evaluation**: runs every 1s, evaluates layout conditions top-to-bottom, sends first match
  - **Template caching**: properties only sent over UART when changed (dirty flag)
  - **Batch command queue**: max 4 commands/batch, 3s response timeout, flow control via in_flight flag
  - **UART parsing**: handles NDJSON responses, robust against kernel printk noise
  - **Page navigation**: `navigate_page_next()`, `navigate_page_prev()`, `navigate_page_jump(n)`
  - **HA integration**: exposes brightness as light entity, auto-registers notification services

### Go Daemon

- `cmd/ulanzi-control/main.go` — Entry point. Framebuffer rendering (720x1280 @ 32bpp), touch/button input handling.
- `pkg/state/store.go` — Global state singleton. ButtonConfig/ButtonState management, RingBuffer for time-series data, thread-safe (RWMutex), state persistence to `config/state.json`.
- `pkg/transport/commands.go` — NDJSON protocol implementation. Modules: `display`, `sensor`, `page`, `system`. Error codes: OK=0, ERROR=1, INVALID=2, TIMEOUT=3, INTERNAL=5.
- `pkg/transport/serial.go` — UART serial communication (1.5 Mbps).
- `pkg/ui/draw.go` + `font.go` — Display rendering pipeline with per-button dirty flags.

### NDJSON Protocol

ESP32 sends commands: `{"type":"command", "module":"display", "action":"set_button_config", "params":{...}}`
D200 responds: `{"type":"response", "status":"ok", "code":0, ...}`
D200 sends events: `{"type":"event", "event":"page_change", ...}` for button presses, page changes, mode changes.

### Key Patterns

- **Conditional rendering**: layouts evaluated top-to-bottom, first matching `when` wins; layout without `when` is the fallback
- **All string properties** accept static strings or `!lambda` returning `optional<std::string>`
- **HA state sync**: template switch (optimistic, calls HA service) + binary_sensor (syncs real state back) — see README for pattern
- **Page -1**: sections visible on all pages (navigation bars, status bars)
