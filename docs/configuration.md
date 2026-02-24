# ulanzi_d200 — Configuration Reference

The `ulanzi_d200` ESPHome component connects an ESP32 to the Ulanzi D200 over UART. It manages **sections** (physical display areas) with **conditional layouts** and **widgets**, sends NDJSON commands over serial, and receives JSON events back.

Entity behavior (switches, buttons) is defined separately using standard ESPHome template entities, connected to sections via events and layout conditions.

## Installation

Add the external component to your ESPHome config:

```yaml
external_components:
  - source: github://mapero/esphome-ulanzi-d200
    components: [ulanzi_d200]
```

## UART Configuration

```yaml
uart:
  id: uart_bus
  tx_pin: GPIO21   # adjust for your board
  rx_pin: GPIO20
  baud_rate: 1500000
  data_bits: 8
  parity: NONE
  stop_bits: 1
```

## Component Configuration

```yaml
ulanzi_d200:
  id: d200
  uart_id: uart_bus

  on_connected:
    then:
      - logger.log: "D200 connected!"
  on_disconnected:
    then:
      - logger.log: "D200 disconnected!"
  on_section_press:
    then:
      - lambda: ESP_LOGI("d200", "Section %d pressed", position);
  on_section_release:
    then:
      - lambda: ESP_LOGI("d200", "Section %d released", position);
  on_page_change:
    then:
      - lambda: |-
          ESP_LOGI("d200", "Page changed to %d (of %d)", page, page_count);
  on_mode_change:
    then:
      - lambda: |-
          ESP_LOGI("d200", "Mode changed to: %s", mode.c_str());

  sections:
    - position: 0
      page: 0
      on_press:
        then:
          - switch.toggle: sw_light
      pressed_style:
        color: "#FF6600"
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
```

## Configuration Reference

### Top-level keys

| Key | Type | Required | Description |
|-----|------|----------|-------------|
| `id` | ID | Yes | Component instance ID |
| `uart_id` | ID | Yes | Reference to UART component |
| `on_connected` | Automation | No | Fires when D200 connection is detected |
| `on_disconnected` | Automation | No | Fires after 10 s with no UART data |
| `on_section_press` | Automation | No | Fires on any section press. Variable: `position` (int) |
| `on_section_release` | Automation | No | Fires on any section release. Variable: `position` (int) |
| `on_page_change` | Automation | No | Fires on page change. Variables: `page` (int), `page_count` (int) |
| `on_mode_change` | Automation | No | Fires when the D200 operating mode changes. Variable: `mode` (string) |
| `sections` | list | No | Section configuration list |

### Section configuration

A section represents a physical display area on the D200 grid (positions 0–13).

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `position` | int | **required** | Physical button index (0–13) |
| `page` | int | `-1` | Page assignment: `-1` = all pages, `0+` = specific page only |
| `on_press` | Automation | — | Fires when this section is pressed |
| `on_release` | Automation | — | Fires when this section is released |
| `pressed_style` | object | `{}` | Visual overrides applied while physically pressed |
| `layouts` | list | **required** | One or more layout definitions |

Notes:
- Position 13 is the double-wide button spanning columns 3–4 in the last row
- `pressed_style` is sent to the D200 upfront so it can be applied instantly without a UART round-trip
- A section with `page: -1` appears on every page
- Sections have no built-in toggle or momentary behavior — they emit raw press/release events

Physical grid layout:

```
Row 0:  [ 0] [ 1] [ 2] [ 3] [ 4]
Row 1:  [ 5] [ 6] [ 7] [ 8] [ 9]
Row 2:  [10] [11] [12] [13 ══════]
```

### Layout configuration

A layout defines what a section displays. Each section must have at least one layout and can have many. The component evaluates layout conditions every 1 second and sends the first matching layout's widget properties to the D200.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `widget` | string | **required** | Widget type: `entity_view`, `gauge`, `line_graph`, `status`, `chips`, `notification` |
| `when` | lambda | — | Condition for this layout. Omit for fallback (always matches) |

Plus widget-specific properties — see [Widget Types](#widget-types) below.

**Layout selection rules:**
1. Layouts are evaluated in order, top to bottom
2. The first layout whose `when` condition returns `true` is displayed
3. A layout with no `when` always matches (use as the last fallback)
4. Conditions are evaluated every 1 second; display updates only when the active layout changes or widget property values change

Example — two layouts for a toggle:

```yaml
layouts:
  # Active layout (shown when switch is on)
  - widget: entity_view
    when: !lambda 'return id(sw_light).state;'
    color: "#FFAA00"
    icon_id: "ceiling-light"
    text: "ON"
  # Default layout (fallback)
  - widget: entity_view
    color: "#333333"
    icon_id: "ceiling-light-outline"
    text: "OFF"
```

### pressed_style

Overrides applied on top of the currently active layout while the section is physically being pressed. Only specify properties you want to override; everything else inherits from the active layout.

| Key | Type | Description |
|-----|------|-------------|
| `color` | string | Background color override |
| `text_color` | string | Primary text color override |
| `text2_color` | string | Secondary text color override |
| `icon_color` | string | Icon color override |
| `style` | string | Animation style override |

## Widget Types

### entity_view

The standard button appearance: icon, primary text, secondary text, background color.

```yaml
- widget: entity_view
  color: "#333333"
  icon_id: "ceiling-light-outline"
  icon_color: "#FFFFFF"
  text: "OFF"
  text_color: "#FFFFFF"
  text2: "Living Room"
  text2_color: "#AAAAAA"
  style: "constant"
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `color` | string/lambda | `"#333333"` | Background color (hex) |
| `icon_id` | string/lambda | `""` | MDI icon name |
| `icon_color` | string/lambda | `"#FFFFFF"` | Icon color |
| `text` | string/lambda | `""` | Primary text |
| `text_color` | string/lambda | `"#FFFFFF"` | Primary text color |
| `text2` | string/lambda | `""` | Secondary text |
| `text2_color` | string/lambda | `"#FFFFFF"` | Secondary text color |
| `style` | string/lambda | `"constant"` | Animation hint (`constant`, `slow`, `fast`) |

### gauge

Circular gauge visualization for a value within a range.

```yaml
- widget: gauge
  min: 0.0
  max: 150.0
  value: !lambda |-
    if (id(sensor_power).has_state())
      return {to_string((int)id(sensor_power).state)};
    return {std::string("0")};
  graph_color: "#8888FF"
  color: "#1A1A2E"
  icon_id: "server"
  icon_color: "#FF8844"
  text: "Server"
  text2: !lambda |-
    if (id(sensor_power).has_state())
      return {format("%.0f W", id(sensor_power).state)};
    return {std::string("-- W")};
  text2_color: "#AAAAAA"
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `min` | float | `0.0` | Gauge minimum value |
| `max` | float | `100.0` | Gauge maximum value |
| `size` | int | `20` | Data point history size (2–100) |
| `value` | string/lambda | `""` | Current numeric value (as string) |
| `graph_color` | string/lambda | `"#FFFFFF"` | Gauge arc fill color |
| `color` | string/lambda | `"#1A1A2E"` | Background color |
| `icon_id` | string/lambda | `""` | Icon displayed in gauge center |
| `icon_color` | string/lambda | `"#FFFFFF"` | Icon color |
| `text` | string/lambda | `""` | Primary label |
| `text_color` | string/lambda | `"#FFFFFF"` | Primary label color |
| `text2` | string/lambda | `""` | Secondary label (typically the formatted value) |
| `text2_color` | string/lambda | `"#FFFFFF"` | Secondary label color |

### line_graph

Line graph showing a value history over time.

```yaml
- widget: line_graph
  min: 15.0
  max: 45.0
  size: 30
  value: !lambda |-
    return {to_string(id(temp_sensor).state)};
  graph_color: "#FF4444"
  color: "#1A1A2E"
  icon_id: "thermometer"
  icon_color: "#FF4444"
  text: "Indoor"
  text2: !lambda |-
    return {format("%.1f C", id(temp_sensor).state)};
  text2_color: "#AAAAAA"
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `min` | float | `0.0` | Y-axis minimum |
| `max` | float | `100.0` | Y-axis maximum |
| `size` | int | `20` | Data point history size (2–100) |
| `value` | string/lambda | `""` | Current numeric value (appended to history every second) |
| `graph_color` | string/lambda | `"#FFFFFF"` | Line color |
| `color` | string/lambda | `"#1A1A2E"` | Background color |
| `icon_id` | string/lambda | `""` | Icon |
| `icon_color` | string/lambda | `"#FFFFFF"` | Icon color |
| `text` | string/lambda | `""` | Primary label |
| `text_color` | string/lambda | `"#FFFFFF"` | Primary label color |
| `text2` | string/lambda | `""` | Secondary label (typically the formatted value) |
| `text2_color` | string/lambda | `"#FFFFFF"` | Secondary label color |

Data points are appended to the D200 graph history once per second. The D200 maintains the buffer.

### status

Status bar for compact multi-item information. Designed for the double-wide position 13.

```yaml
- widget: status
  items:
    - clock
    - page
  color: "#1A1A2E"
  text_color: "#AACCFF"
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `items` | list of strings | **required** | Status elements to display |
| `color` | string/lambda | `"#1A1A2E"` | Background color |
| `text_color` | string/lambda | `"#FFFFFF"` | Text color |

Built-in items: `clock` (current time), `page` (page X/Y). Unknown items render as plain text.

### chips

Chip/tag row showing multiple small labeled items. Designed for the double-wide position 13.

```yaml
- widget: chips
  color: "#1A1A2E"
  chips:
    - icon_id: "wifi"
      label: "WiFi"
      bg_color: "#333333"
    - icon_id: "thermometer"
      label: "24.5 C"
      bg_color: "#443322"
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `color` | string/lambda | `"#1A1A2E"` | Background color |
| `chips` | list | **required** | Chip definitions |

Each chip:

| Key | Type | Description |
|-----|------|-------------|
| `icon_id` | string | MDI icon name |
| `label` | string | Chip text |
| `bg_color` | string | Chip background color (text is auto-contrasted) |

### notification

A section that receives push notifications from Home Assistant via an auto-registered HA service. When a notification arrives it is displayed for a few seconds and then the section reverts to its normal layout.

```yaml
- widget: notification
  service_id: doorbell_alert
  color: "#1A1A2E"
  icon_id: "bell"
  icon_color: "#FFCC44"
  text: "Doorbell"
```

| Property | Type | Default | Description |
|----------|------|---------|-------------|
| `service_id` | string | **required** | Unique identifier; becomes a HA service `ulanzi_d200.<service_id>` |
| `color` | string/lambda | `"#1A1A2E"` | Default background color |
| `icon_id` | string/lambda | `""` | Icon |
| `icon_color` | string/lambda | `"#FFFFFF"` | Icon color |
| `text` | string/lambda | `""` | Text shown when idle |

Call the service from HA automations:

```yaml
service: ulanzi_d200.doorbell_alert
data:
  message: "Someone at the door"
```

## Template Lambdas

All string widget properties accept either a static string or a `!lambda` returning `optional<std::string>`:

```yaml
text: !lambda |-
  if (id(temp_sensor).has_state() && !isnan(id(temp_sensor).state)) {
    char buf[16];
    snprintf(buf, sizeof(buf), "%.1f C", id(temp_sensor).state);
    return {std::string(buf)};
  }
  return {std::string("-- C")};
```

- Lambdas are evaluated every 1 second
- Display updates only when a value actually changes (deduplication)
- Return `{std::string("value")}` to provide a value
- Return `{}` to keep the previous/static value

## Automation Triggers

### on_connected

Fires when the ESP32 first detects a response from the D200. Use this to send initial configuration.

```yaml
on_connected:
  then:
    - delay: 500ms
    - lambda: |-
        id(d200).set_brightness(80);
        id(d200).send_command("page", "rename", "0", "Home");
```

### on_disconnected

Fires after 10 seconds with no UART data from the D200.

### on_section_press (component-level)

Fires when any section is pressed. Available variable: `position` (int, 0–13).

### on_section_release (component-level)

Fires when any section is released. Available variable: `position` (int, 0–13).

### on_page_change

Fires when the current page changes. Variables: `page` (int, new page index), `page_count` (int, total pages).

### on_mode_change

Fires when the D200 operating mode changes. Variable: `mode` (string).

### Per-section on_press / on_release

Each section independently emits press and release events. Sections have no built-in toggle or momentary behavior — implement it in the automation.

```yaml
sections:
  - position: 0
    on_press:
      then:
        - switch.toggle: sw_light
    on_release:
      then:
        - logger.log: "Button 0 released"
```

## Entity Integration

### Template Switch (toggle behavior)

Use a standard ESPHome template switch for bidirectional toggle behavior:

```yaml
switch:
  - platform: template
    id: sw_light
    name: "Living Room Light"
    optimistic: true
    turn_on_action:
      - homeassistant.service:
          service: light.turn_on
          data:
            entity_id: light.living_room
    turn_off_action:
      - homeassistant.service:
          service: light.turn_off
          data:
            entity_id: light.living_room
```

Connect to a section:

```yaml
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
```

### Home Assistant State Sync

Sync HA entity state back to the ESPHome switch using the `homeassistant` binary sensor platform:

```yaml
binary_sensor:
  - platform: homeassistant
    entity_id: light.living_room
    trigger_on_initial_state: true
    on_state:
      then:
        - lambda: |-
            if (x) id(sw_light).turn_on();
            else id(sw_light).turn_off();
```

This creates a complete bidirectional sync loop:
1. **User presses D200** → `on_press` → `switch.toggle` → HA service call
2. **HA state changes** → `homeassistant` binary_sensor → lambda updates switch → layout condition re-evaluated → display updates

### Template Button (momentary action)

For one-shot actions with no state:

```yaml
button:
  - platform: template
    id: btn_garage
    name: "Garage Door"
    on_press:
      - homeassistant.service:
          service: cover.toggle
          data:
            entity_id: cover.garage_door
```

Connect to a section:

```yaml
sections:
  - position: 3
    page: 0
    on_press:
      then:
        - button.press: btn_garage
    layouts:
      - widget: entity_view
        icon_id: "garage"
        text: "Garage"
        color: "#442222"
    pressed_style:
      color: "#FF4444"
```

### Page Navigation

```yaml
sections:
  # Next page
  - position: 4
    page: -1
    on_press:
      then:
        - lambda: id(d200).navigate_page_next();
    layouts:
      - widget: entity_view
        icon_id: "chevron-right"
        icon_color: "#5555AA"
        text: "NEXT"
        color: "#111122"
    pressed_style:
      color: "#4444AA"

  # Previous page
  - position: 9
    page: -1
    on_press:
      then:
        - lambda: id(d200).navigate_page_prev();
    layouts:
      - widget: entity_view
        icon_id: "chevron-left"
        icon_color: "#5555AA"
        text: "PREV"
        color: "#111122"
    pressed_style:
      color: "#4444AA"

  # Jump to a specific page (highlighted when active)
  - position: 10
    page: -1
    on_press:
      then:
        - lambda: id(d200).navigate_page_jump(0);
    layouts:
      - widget: entity_view
        when: !lambda 'return id(d200).get_current_page() == 0;'
        icon_id: "home"
        icon_color: "#FF9944"
        text: "Home"
        text_color: "#FF9944"
        color: "#2A1A0A"
      - widget: entity_view
        icon_id: "home-outline"
        icon_color: "#444444"
        text: "Home"
        text_color: "#444444"
        color: "#111111"
```

### Sensor Visualization (display only)

Omit `on_press` entirely for read-only sections:

```yaml
sections:
  - position: 1
    page: 0
    layouts:
      - widget: gauge
        min: 0.0
        max: 150.0
        value: !lambda |-
          return {to_string((int)id(ha_power_server).state)};
        graph_color: "#FF8844"
        icon_id: "server"
        text: "Server"
        text2: !lambda |-
          return {format("%.0f W", id(ha_power_server).state)};
        color: "#1A1A2E"
```

## Advanced Patterns

### Multi-Widget Layout Switching

A section can switch between completely different widget types based on conditions:

```yaml
sections:
  - position: 1
    page: 0
    layouts:
      # Show gauge while progress is active
      - widget: gauge
        when: !lambda 'return id(progress).state > 0;'
        min: 0.0
        max: 100.0
        value: !lambda 'return {to_string((int)id(progress).state)};'
        graph_color: "#8888FF"
        icon_id: "printer-3d-nozzle"
        text: "Printing"
        color: "#1A1A2E"
      # Fall back to idle state
      - widget: entity_view
        icon_id: "printer-3d"
        icon_color: "#666666"
        text: "Idle"
        color: "#1A1A2E"
```

### Cycling Layouts

Use `set_section_layout()` to programmatically cycle through layouts on press:

```yaml
sections:
  - position: 2
    page: 0
    on_press:
      then:
        - lambda: |-
            int current = id(d200).get_active_layout(2);
            int next = (current + 1) % 3;
            id(d200).set_section_layout(2, next);
    layouts:
      - widget: line_graph
        min: 15.0
        max: 45.0
        value: !lambda 'return {to_string(id(ha_temp).state)};'
        graph_color: "#FF8844"
        text: "Temperature"
        color: "#1A1A2E"
      - widget: gauge
        min: 0.0
        max: 100.0
        value: !lambda 'return {to_string((int)id(ha_battery).state)};'
        graph_color: "#44FF44"
        icon_id: "battery"
        text: "Battery"
        color: "#1A1A2E"
      - widget: entity_view
        icon_id: "memory"
        text: !lambda 'return {format("CPU %.0f%%", id(ha_cpu).state)};'
        color: "#1A1A2E"
```

When `set_section_layout()` is used, automatic condition evaluation is overridden for that section until the next condition-driven change.

## Home Assistant Services

Define HA-callable services in the ESPHome `api:` block:

```yaml
api:
  services:
    - service: set_brightness
      variables:
        brightness: int
      then:
        - lambda: id(d200).set_brightness(brightness);

    - service: toggle_display
      then:
        - lambda: id(d200).toggle_display();

    - service: page_next
      then:
        - lambda: id(d200).navigate_page_next();

    - service: page_prev
      then:
        - lambda: id(d200).navigate_page_prev();

    - service: page_jump
      variables:
        page: int
      then:
        - lambda: id(d200).navigate_page_jump(page);

    - service: send_command
      variables:
        module: string
        action: string
        param1: string
        param2: string
      then:
        - lambda: |-
            id(d200).send_command(module.c_str(), action.c_str(),
                                  param1.empty() ? nullptr : param1.c_str(),
                                  param2.empty() ? nullptr : param2.c_str());
```

## C++ API

These methods are available on the component instance (accessed via `id(d200).method()`):

| Method | Description |
|--------|-------------|
| `set_brightness(uint8_t)` | Set display brightness (0–100) |
| `toggle_display()` | Toggle display on/off |
| `send_command(module, action, p1, p2, p3)` | Send an arbitrary NDJSON command |
| `send_ping()` | Ping the D200 daemon |
| `reset_all_buttons()` | Clear all button configs on the D200 |
| `send_sensor_reading(sensor, value)` | Push a sensor value to the D200 |
| `sync_time(RealTimeClock*)` | Sync D200 clock from an ESPHome time source |
| `navigate_page_next()` | Go to next page |
| `navigate_page_prev()` | Go to previous page |
| `navigate_page_jump(page)` | Jump to specific page |
| `is_connected()` | Returns `true` if D200 is connected |
| `get_current_page()` | Get current page index |
| `get_page_count()` | Get total page count |
| `set_section_layout(position, layout_index)` | Programmatically select a layout by index |
| `get_active_layout(position)` | Get the currently active layout index for a section |

## Auto-created Entities

### Display Backlight

The component automatically registers a **brightness-only light entity** named `Display Backlight`. It appears in Home Assistant as a dimmable light (no color, brightness only). Restore mode is `RESTORE_DEFAULT_ON`.

No other entities are auto-created. All interactive entities (switches, buttons, sensors) are defined using standard ESPHome platform entities in your YAML.

## Suggested Optional Entities

These are common patterns you can add alongside the component:

| Entity | Platform | Description |
|--------|----------|-------------|
| `Connected` | Binary Sensor (template) | D200 connection status via `is_connected()` |
| `Current Page` | Sensor (template) | Current page via `get_current_page()` |
| `Brightness` | Number (template) | Brightness control via `set_brightness()` |
| `WiFi Signal` | Sensor (wifi_signal) | ESP32 WiFi RSSI |
| `Uptime` | Sensor (uptime) | ESP32 uptime |
| `ESP32 Temperature` | Sensor (internal_temperature) | ESP32 chip temperature |
| `IP Address` | Text Sensor (wifi_info) | ESP32 IP address |

## Examples

- [`examples/basic.yaml`](../examples/basic.yaml) — Toggle switches, momentary buttons, page navigation, HA state sync
- [`examples/sensors.yaml`](../examples/sensors.yaml) — Sensor dashboard with gauges, line graphs, chips, and conditional widget switching
