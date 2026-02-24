# esphome-ulanzi-d200

ESPHome external component for the **Ulanzi D200** stream deck. Connects an ESP32 to the D200 display over UART and exposes a declarative YAML interface for building interactive dashboards.

## What it does

The D200 is a physical stream deck with a 14-button capacitive grid (5×3 layout, button 13 is double-wide). An ESP32 bridges the D200 to Home Assistant over WiFi using this component.

- Send display commands to the D200 over UART (1.5 Mbps NDJSON)
- Define what each button shows using **sections**, **layouts**, and **widgets**
- React to button presses and releases with ESPHome automations
- Sync state bidirectionally with Home Assistant entities
- Auto-expose a display brightness light and optional diagnostic sensors

## Hardware

- **Display controller:** Ulanzi D200 (RK3308, running Linux)
- **Bridge MCU:** Any ESP32 with two free UART pins
  - Tested: Seeed XIAO ESP32-C3
- **Connection:** UART, 3.3 V logic, 1.5 Mbps

```
Row 0:  [ 0] [ 1] [ 2] [ 3] [ 4]
Row 1:  [ 5] [ 6] [ 7] [ 8] [ 9]
Row 2:  [10] [11] [12] [13 ══════]
```

Button 13 is double-wide (spans columns 3–4 in the last row) and is typically used for the status bar.

## Quick Start

1. **Add the external component** to your ESPHome config:

```yaml
external_components:
  - source: github://mapero/esphome-ulanzi-d200
    components: [ulanzi_d200]
```

2. **Configure UART** (adjust pins for your board):

```yaml
uart:
  id: uart_bus
  tx_pin: GPIO21
  rx_pin: GPIO20
  baud_rate: 1500000
  data_bits: 8
  parity: NONE
  stop_bits: 1
```

3. **Define sections:**

```yaml
ulanzi_d200:
  id: d200
  uart_id: uart_bus

  on_connected:
    then:
      - lambda: id(d200).set_brightness(80);

  sections:
    - position: 0
      page: 0
      on_press:
        then:
          - switch.toggle: sw_light
      layouts:
        - widget: entity_view
          when: !lambda 'return id(sw_light).state;'
          color: "#FFAA00"
          icon_id: "ceiling-light"
          text: "ON"
          text2: "Living Room"
        - widget: entity_view
          color: "#333333"
          icon_id: "ceiling-light-outline"
          text: "OFF"
          text2: "Living Room"

    - position: 13
      page: -1
      layouts:
        - widget: status
          items: [clock, page]
          color: "#111122"
          text_color: "#AACCFF"
```

See [`examples/basic.yaml`](examples/basic.yaml) for a complete working configuration.

## Concepts

### Sections

A **section** maps to a physical button position (0–13). Each section can be assigned to a specific page (`page: 0`, `page: 1`, …) or shown on all pages (`page: -1`). Sections emit `on_press` and `on_release` events for automations.

### Layouts

Each section has one or more **layouts**. The component evaluates layout conditions (`when: !lambda ...`) every second and displays the first matching layout. A layout without `when` is always shown (use as fallback). This enables toggle and state-driven display without extra glue code.

### Widgets

Each layout uses a **widget type** that determines how it renders:

| Widget | Description |
|--------|-------------|
| `entity_view` | Standard button: icon + primary text + secondary text |
| `gauge` | Circular gauge with value history |
| `line_graph` | Line graph with scrolling value history |
| `status` | Status bar with clock, page indicator (for pos 13) |
| `chips` | Compact chip row (for pos 13) |
| `notification` | Push notification receiver (HA service integration) |

All string properties (`text`, `color`, `icon_id`, …) accept either a static string or a `!lambda` returning `optional<std::string>`, evaluated every second.

### Pages

Pages group sections into logical screens. Navigate between them using `navigate_page_next()`, `navigate_page_prev()`, or `navigate_page_jump(n)`. Use `page: -1` for buttons that should always be visible (navigation bar, status bar).

### HA State Sync

The recommended pattern for toggle buttons:

```yaml
# ESPHome switch calls HA service (optimistic, updates immediately)
switch:
  - platform: template
    id: sw_light
    optimistic: true
    turn_on_action:
      - homeassistant.service: {service: light.turn_on, data: {entity_id: light.living_room}}
    turn_off_action:
      - homeassistant.service: {service: light.turn_off, data: {entity_id: light.living_room}}

# HA binary_sensor syncs real state back to the switch
binary_sensor:
  - platform: homeassistant
    entity_id: light.living_room
    trigger_on_initial_state: true
    on_state:
      then:
        - lambda: if (x) id(sw_light).turn_on(); else id(sw_light).turn_off();
```

## Examples

| File | Description |
|------|-------------|
| [`examples/basic.yaml`](examples/basic.yaml) | Two-page setup: toggle switches, momentary buttons, page navigation, HA state sync |
| [`examples/sensors.yaml`](examples/sensors.yaml) | Two-page sensor dashboard: gauges, line graphs, chips, conditional widget switching |

Copy `examples/secrets.yaml.example` to `examples/secrets.yaml` and fill in your credentials before flashing.

## Configuration Reference

Full documentation: [docs/configuration.md](docs/configuration.md)

## Repository Layout

```
components/
  ulanzi_d200/
    __init__.py         ESPHome component definition (schema + codegen)
    ulanzi_d200.h       C++ class declaration
    ulanzi_d200.cpp     C++ implementation
docs/
  configuration.md      Full configuration reference
examples/
  basic.yaml            Toggle/button/navigation example
  sensors.yaml          Sensor dashboard example
  secrets.yaml.example  Template for secrets.yaml
```

## License

MIT
