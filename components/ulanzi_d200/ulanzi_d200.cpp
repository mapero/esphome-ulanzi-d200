#include "ulanzi_d200.h"
#include "esphome/core/log.h"
#include "esphome/core/helpers.h"
#include "esphome/core/application.h"
#ifdef USE_TIME
#include "esphome/components/time/real_time_clock.h"
#endif
#ifdef USE_API_SERVICES
#include "esphome/components/api/api_server.h"
#include "esphome/components/api/user_services.h"
#include "esphome/core/base_automation.h"
#endif

namespace esphome {
namespace ulanzi_d200 {

static const char *TAG = "ulanzi_d200";

ConnectedTrigger::ConnectedTrigger(UlanziBridge *parent) {
  parent->add_on_connected_callback([this]() { this->trigger(); });
}

DisconnectedTrigger::DisconnectedTrigger(UlanziBridge *parent) {
  parent->add_on_disconnected_callback([this]() { this->trigger(); });
}

SectionPressTrigger::SectionPressTrigger(UlanziBridge *parent) {
  parent->add_on_section_press_callback([this](int position) { this->trigger(position); });
}

SectionReleaseTrigger::SectionReleaseTrigger(UlanziBridge *parent) {
  parent->add_on_section_release_callback([this](int position) { this->trigger(position); });
}

// UlanziBacklightLight - called when HA/ESPHome changes the light
void UlanziBacklightLight::write_state(light::LightState *state) {
  float brightness;
  state->current_values_as_brightness(&brightness);

  bool is_on;
  state->current_values_as_binary(&is_on);

  int pct = is_on ? (int)(brightness * 100.0f) : 0;
  this->parent_->set_brightness((uint8_t)pct);
}

PageChangeTrigger::PageChangeTrigger(UlanziBridge *parent) {
  parent->add_on_page_change_callback([this](int page, int page_count) {
    this->trigger(page, page_count);
  });
}

ModeChangeTrigger::ModeChangeTrigger(UlanziBridge *parent) {
  parent->add_on_mode_change_callback([this](const std::string &mode) {
    this->trigger(mode);
  });
}

void UlanziBridge::setup() {
  // Send initial ping after setup
  this->set_timeout(2000, [this]() {
    this->send_ping();
  });

  // Fallback: send configs after 3s if ready event hasn't arrived yet
  this->set_timeout(3000, [this]() {
    if (!this->configs_sent_) {
      ESP_LOGI("ulanzi_d200", "No ready event received, sending configs via timeout fallback");
      this->reset_all_buttons();
      this->send_all_section_configs_();
    }
  });

  // Register notification services with the API server
#ifdef USE_API_SERVICES
  for (auto &svc : this->notification_services_) {
    auto *trigger = new api::UserServiceTrigger<std::string>(
        svc.name, std::array<std::string, 1>{{"message"}});
    int btn = svc.button_id;
    int pg = svc.page_id;
    auto *automation_obj = new Automation<std::string>(trigger);
    auto *action = new LambdaAction<std::string>([this, btn, pg](std::string message) {
      this->send_notification(btn, pg, message.c_str());
    });
    automation_obj->add_actions({action});
    api::global_api_server->register_user_service(trigger);
    ESP_LOGI(TAG, "Registered API service '%s' (button %d, page %d)", svc.name.c_str(), btn, pg);
  }
#endif

  // Set up interval for section/layout evaluation (every 1 second)
  this->set_interval(1000, [this]() {
    this->evaluate_and_update_sections_();
  });
}

void UlanziBridge::loop() {
  // Process incoming UART data (responses from Ulanzi)
  this->process_uart_data_();

  // Check connection timeout
  this->check_connection_timeout_();

  // Flush any pending batched commands
  this->flush_batch_();

  // Periodic ping for health monitoring (keeps connection alive)
  if (this->connected_ && millis() - this->last_ping_time_ > PING_INTERVAL_MS) {
    this->last_ping_time_ = millis();
    this->send_ping();
  }
}

void UlanziBridge::dump_config() {
  ESP_LOGCONFIG(TAG, "Ulanzi D200 (Direct NDJSON):");
  ESP_LOGCONFIG(TAG, "  Connected: %s", YESNO(this->connected_));
  ESP_LOGCONFIG(TAG, "  Mode: Bidirectional NDJSON");
  ESP_LOGCONFIG(TAG, "  Current Page: %d", this->current_page_);
  ESP_LOGCONFIG(TAG, "  Page Count: %d", this->page_count_);
  ESP_LOGCONFIG(TAG, "  Current Mode: %s", this->current_mode_.c_str());
  ESP_LOGCONFIG(TAG, "  Sections: %d", this->sections_.size());
}

void UlanziBridge::process_uart_data_() {
  while (this->available()) {
    uint8_t c;
    this->read_byte(&c);

    // Response messages are newline-delimited
    if (c == '\n') {
      if (!rx_buffer_.empty()) {
        this->parse_response_line_(rx_buffer_);
        rx_buffer_.clear();
      }
    } else if (c != '\r') {
      rx_buffer_ += (char)c;

      // Prevent buffer overflow
      if (rx_buffer_.size() > MAX_BUFFER_SIZE) {
        ESP_LOGW(TAG, "RX buffer overflow, clearing");
        rx_buffer_.clear();
      }
    }
  }
}

void UlanziBridge::parse_response_line_(const std::string &line) {
  // Update last message time
  last_message_time_ = millis();

  // Search for JSON anywhere in the line (robust against kernel printk noise)
  size_t json_start = line.find('{');
  if (json_start == std::string::npos) {
    return;  // No JSON on this line, ignore
  }
  size_t json_end = line.rfind('}');
  if (json_end == std::string::npos || json_end <= json_start) {
    return;
  }

  std::string json = line.substr(json_start, json_end - json_start + 1);

  // Peek at "type" field to route the message
  size_t type_pos = json.find("\"type\":\"");
  if (type_pos == std::string::npos) {
    // No type field - try parsing as event (legacy)
    this->parse_json_event_(json);
    return;
  }

  size_t type_value_start = type_pos + 8;  // skip past "type":"
  size_t type_value_end = json.find("\"", type_value_start);
  if (type_value_end == std::string::npos) {
    this->parse_json_event_(json);
    return;
  }

  std::string type = json.substr(type_value_start, type_value_end - type_value_start);

  if (type == "response" || type == "batch_response") {
    // Command response from daemon — clear batch flow control
    this->batch_in_flight_ = false;

    // Any valid response means the daemon is alive
    if (!connected_) {
      connected_ = true;
      ESP_LOGI(TAG, "Ulanzi D200 connected!");
      on_connected_callbacks_.call();
    }
  } else {
    // Event messages (event, ready, etc.) — route to event parser
    this->parse_json_event_(json);
  }
}

void UlanziBridge::parse_json_event_(const std::string &json) {
  // Check for ready event: {"type":"ready","page":N,"page_count":M}
  if (json.find("\"ready\"") != std::string::npos) {
    // Extract page number
    int page = 0;
    size_t page_pos = json.find("\"page\":");
    if (page_pos != std::string::npos) {
      page_pos += 7;
      while (page_pos < json.length() && (json[page_pos] == ' ' || json[page_pos] == '\t')) {
        page_pos++;
      }
      while (page_pos < json.length() && json[page_pos] >= '0' && json[page_pos] <= '9') {
        page = page * 10 + (json[page_pos] - '0');
        page_pos++;
      }
    }

    // Extract page_count
    int page_count = this->page_count_;
    size_t count_pos = json.find("\"page_count\":");
    if (count_pos != std::string::npos) {
      count_pos += 13;
      while (count_pos < json.length() && (json[count_pos] == ' ' || json[count_pos] == '\t')) {
        count_pos++;
      }
      page_count = 0;
      while (count_pos < json.length() && json[count_pos] >= '0' && json[count_pos] <= '9') {
        page_count = page_count * 10 + (json[count_pos] - '0');
        count_pos++;
      }
    }

    this->handle_ready_(page, page_count);
    return;
  }

  // Check for page_change event: {"type":"page_change","page":N,"page_count":M}
  if (json.find("\"page_change\"") != std::string::npos ||
      json.find("\"type\":\"page_change\"") != std::string::npos) {
    // Extract page number
    size_t page_pos = json.find("\"page\":");
    if (page_pos != std::string::npos) {
      page_pos += 7;
      while (page_pos < json.length() && (json[page_pos] == ' ' || json[page_pos] == '\t')) {
        page_pos++;
      }
      int page = 0;
      while (page_pos < json.length() && json[page_pos] >= '0' && json[page_pos] <= '9') {
        page = page * 10 + (json[page_pos] - '0');
        page_pos++;
      }

      // Extract page_count
      int page_count = this->page_count_;
      size_t count_pos = json.find("\"page_count\":");
      if (count_pos != std::string::npos) {
        count_pos += 13;
        while (count_pos < json.length() && (json[count_pos] == ' ' || json[count_pos] == '\t')) {
          count_pos++;
        }
        page_count = 0;
        while (count_pos < json.length() && json[count_pos] >= '0' && json[count_pos] <= '9') {
          page_count = page_count * 10 + (json[count_pos] - '0');
          count_pos++;
        }
      }

      // Extract label (optional): "label":"some name"
      std::string label;
      size_t label_pos = json.find("\"label\":\"");
      if (label_pos != std::string::npos) {
        label_pos += 9; // skip past "label":"
        size_t label_end = json.find("\"", label_pos);
        if (label_end != std::string::npos) {
          label = json.substr(label_pos, label_end - label_pos);
        }
      }

      ESP_LOGD(TAG, "Page change event: page=%d, page_count=%d, label=%s", page, page_count, label.c_str());
      this->handle_page_change_(page, page_count, label);
      return;
    }
  }

  // Check for mode_change event: {"type":"event","payload":{"type":"mode_change","mode":"..."}}
  if (json.find("\"mode_change\"") != std::string::npos) {
    size_t mode_pos = json.find("\"mode\":\"");
    if (mode_pos != std::string::npos) {
      mode_pos += 8; // skip past "mode":"
      size_t mode_end = json.find("\"", mode_pos);
      if (mode_end != std::string::npos) {
        std::string new_mode = json.substr(mode_pos, mode_end - mode_pos);
        std::string old_mode = this->current_mode_;
        this->current_mode_ = new_mode;
        ESP_LOGI(TAG, "Mode change event: %s -> %s", old_mode.c_str(), new_mode.c_str());
        this->on_mode_change_callbacks_.call(new_mode);

        // When returning to run mode, re-send configs so the display is correct
        if (new_mode == "run" && old_mode != "run") {
          this->set_timeout(100, [this]() {
            this->reset_all_buttons();
            this->send_all_section_configs_();
          });
        }
      }
    }
    return;
  }

  // Check for brightness event: {"type":"brightness","value":N}
  if (json.find("\"brightness\"") != std::string::npos) {
    size_t val_pos = json.find("\"value\":");
    if (val_pos != std::string::npos) {
      val_pos += 8;
      while (val_pos < json.length() && (json[val_pos] == ' ' || json[val_pos] == '\t')) {
        val_pos++;
      }
      int pct = 0;
      while (val_pos < json.length() && json[val_pos] >= '0' && json[val_pos] <= '9') {
        pct = pct * 10 + (json[val_pos] - '0');
        val_pos++;
      }
      ESP_LOGD(TAG, "Brightness event: %d%%", pct);
      this->update_backlight_brightness(pct);
      return;
    }
  }

  // Button events: {"type":"event","payload":{"btn":N,"state":"..."}}
  size_t btn_pos = json.find("\"btn\":");
  if (btn_pos == std::string::npos) {
    return;
  }

  // Extract button ID number
  btn_pos += 6; // Skip '"btn":'
  while (btn_pos < json.length() && (json[btn_pos] == ' ' || json[btn_pos] == '\t')) {
    btn_pos++;
  }

  int button_id = 0;
  while (btn_pos < json.length() && json[btn_pos] >= '0' && json[btn_pos] <= '9') {
    button_id = button_id * 10 + (json[btn_pos] - '0');
    btn_pos++;
  }

  // Extract state value
  size_t state_pos = json.find("\"state\":");
  if (state_pos == std::string::npos) {
    ESP_LOGW(TAG, "No 'state' field in JSON event");
    return;
  }

  size_t state_value_start = json.find("\"", state_pos + 8);
  if (state_value_start == std::string::npos) {
    ESP_LOGW(TAG, "Invalid state value format");
    return;
  }
  state_value_start++;

  size_t state_value_end = json.find("\"", state_value_start);
  if (state_value_end == std::string::npos) {
    ESP_LOGW(TAG, "Invalid state value format");
    return;
  }

  std::string state = json.substr(state_value_start, state_value_end - state_value_start);

  // Find the section for this physical position
  int si = this->find_section_for_position_(button_id);

  if (state == "pressed") {
    ESP_LOGD(TAG, "Section position %d pressed (section %d)", button_id, si);

    // Fire per-section press triggers
    if (si >= 0) {
      auto it = this->section_press_triggers_.find(si);
      if (it != this->section_press_triggers_.end()) {
        for (auto *trigger : it->second) {
          trigger->trigger();
        }
      }
    }

    // Fire component-level on_section_press
    on_section_press_callbacks_.call(button_id);

  } else if (state == "released") {
    ESP_LOGD(TAG, "Section position %d released (section %d)", button_id, si);

    // Fire per-section release triggers
    if (si >= 0) {
      auto it = this->section_release_triggers_.find(si);
      if (it != this->section_release_triggers_.end()) {
        for (auto *trigger : it->second) {
          trigger->trigger();
        }
      }
    }

    // Fire component-level on_section_release
    on_section_release_callbacks_.call(button_id);

    // Deferred layout re-evaluation: allow switch state changes from on_press
    // to propagate before re-evaluating layouts
    if (si >= 0) {
      int captured_si = si;
      this->set_timeout(10, [this, captured_si]() {
        int new_layout = this->evaluate_active_layout_(captured_si);
        int old_layout = this->active_layout_index_[captured_si];
        if (new_layout != old_layout) {
          this->active_layout_index_[captured_si] = new_layout;
          this->last_template_values_.erase(captured_si);
          this->send_section_config_(captured_si);
        }
      });
    }

  } else if (state == "active" || state == "inactive") {
    // D200 won't send these for momentary type, but handle gracefully
    // Momentary type — ignore active/inactive
  } else {
    ESP_LOGW(TAG, "Unknown button state: %s", state.c_str());
  }
}

void UlanziBridge::check_connection_timeout_() {
  if (connected_ && (millis() - last_message_time_ > CONNECTION_TIMEOUT_MS)) {
    ESP_LOGW(TAG, "Connection timeout, marking as disconnected");
    connected_ = false;
    this->batch_in_flight_ = false;  // Allow sends on reconnect
    this->configs_sent_ = false;     // Re-send configs when reconnected
    on_disconnected_callbacks_.call();
  }
}

// Page management
void UlanziBridge::handle_page_change_(int page, int page_count, const std::string &label) {
  int old_page = this->current_page_;
  this->current_page_ = page;
  this->page_count_ = page_count;
  this->page_label_ = label;

  ESP_LOGD(TAG, "Page changed: %d -> %d (total: %d, label: %s)", old_page, page, page_count, label.c_str());

  // Re-send sections for the new page
  this->send_sections_for_page_(page);

  // Fire page change callbacks
  this->on_page_change_callbacks_.call(page, page_count);
}

void UlanziBridge::send_sections_for_page_(int page) {
  // Re-send section configs for the new page

  for (int i = 0; i < (int)this->sections_.size(); i++) {
    if (!this->is_section_on_page_(this->sections_[i], page)) {
      continue;
    }

    // Evaluate active layout and send config
    int layout_idx = this->evaluate_active_layout_(i);
    this->active_layout_index_[i] = layout_idx;
    this->last_template_values_.erase(i);
    this->send_section_config_(i);
  }
}

bool UlanziBridge::is_section_on_page_(const SectionConfig &section, int page) const {
  if (section.page < 0) {
    return true;
  }
  return section.page == page;
}

int UlanziBridge::find_section_for_position_(int position) const {
  int fallback = -1;
  for (int i = 0; i < (int)this->sections_.size(); i++) {
    if (this->sections_[i].position == position) {
      if (this->sections_[i].page == this->current_page_) return i;
      if (this->sections_[i].page < 0) fallback = i;
    }
  }
  return fallback;
}

void UlanziBridge::navigate_page_next() {
  this->send_command("page", "next");
}

void UlanziBridge::navigate_page_prev() {
  this->send_command("page", "prev");
}

void UlanziBridge::navigate_page_jump(int target_page) {
  char page_str[8];
  snprintf(page_str, sizeof(page_str), "%d", target_page);
  this->send_command("page", "set", page_str);
}

void UlanziBridge::set_mode(const std::string &mode) {
  ESP_LOGD(TAG, "Setting mode to: %s", mode.c_str());
  this->send_command("system", "set_mode", mode.c_str());
}

void UlanziBridge::configure_button_on_page(int button_id, int page_id, const char *json_config) {
  // Build set_config command and queue for batching
  char id_str[8];
  snprintf(id_str, sizeof(id_str), "%d", button_id);
  char page_str[8];
  snprintf(page_str, sizeof(page_str), "%d", page_id);

  // Build IPC JSON with 4 params: button_id, json_config, persist, page_id
  std::string json = "{\"module\":\"button\",\"action\":\"set_config\",\"params\":[\"";
  json += id_str;
  json += "\",\"";
  // JSON-escape the json_config (contains nested JSON)
  for (const char *p = json_config; *p; p++) {
    if (*p == '"') json += "\\\"";
    else if (*p == '\\') json += "\\\\";
    else json += *p;
  }
  json += "\",\"false\",\"";
  json += page_str;
  json += "\"]}";

  this->pending_batch_.push_back(std::move(json));
}

// Flush pending commands as NDJSON directly to serial.
// Only one batch is in flight at a time to avoid overflowing serial buffers.
void UlanziBridge::flush_batch_() {
  if (this->pending_batch_.empty()) return;

  // Flow control: wait for previous batch to be processed
  if (this->batch_in_flight_) {
    if (millis() - this->batch_sent_time_ < BATCH_TIMEOUT_MS) {
      return;  // Still waiting for response or timeout
    }
    ESP_LOGW(TAG, "Batch response timeout after %dms, forcing next batch", BATCH_TIMEOUT_MS);
    this->batch_in_flight_ = false;
  }

  // Take at most MAX_BATCH_COMMANDS from the queue to keep payload small
  int count = std::min((int)this->pending_batch_.size(), MAX_BATCH_COMMANDS);

  // Build batch JSON envelope
  std::string json = "{\"type\":\"batch\",\"commands\":[";
  for (int i = 0; i < count; i++) {
    if (i > 0) json += ",";
    json += this->pending_batch_[i];
  }
  json += "]}";

  // Remove sent commands from queue
  this->pending_batch_.erase(this->pending_batch_.begin(), this->pending_batch_.begin() + count);

  // Write NDJSON directly to serial
  this->write_str(json.c_str());
  this->write_byte('\n');

  // Mark batch as in flight
  this->batch_in_flight_ = true;
  this->batch_sent_time_ = millis();
}

// Generic command sender - builds IPC JSON and queues for batch flush
void UlanziBridge::send_command(const char *module, const char *action,
                                 const char *param1, const char *param2, const char *param3) {
  // JSON-escape helper for string values
  auto json_esc = [](std::string &out, const char *s) {
    for (const char *p = s; *p; p++) {
      if (*p == '"') out += "\\\"";
      else if (*p == '\\') out += "\\\\";
      else out += *p;
    }
  };

  std::string json = "{\"module\":\"";
  json += module;
  json += "\",\"action\":\"";
  json += action;
  json += "\",\"params\":[";

  const char *params[] = {param1, param2, param3};
  bool first = true;
  for (int i = 0; i < 3; i++) {
    if (params[i] != nullptr && params[i][0] != '\0') {
      if (!first) json += ",";
      json += "\"";
      json_esc(json, params[i]);
      json += "\"";
      first = false;
    }
  }

  json += "]}";

  this->pending_batch_.push_back(std::move(json));
}

// Ping
void UlanziBridge::send_ping() {
  this->send_command("system", "ping");
}

// Reset all buttons on D200
void UlanziBridge::reset_all_buttons() {
  ESP_LOGI(TAG, "Resetting all D200 buttons to defaults");
  this->send_command("button", "reset_all");
}

// Set brightness
void UlanziBridge::set_brightness(uint8_t brightness) {
  int pct = (int)brightness;
  if (pct == this->last_brightness_sent_) return;
  this->last_brightness_sent_ = pct;
  char value[8];
  snprintf(value, sizeof(value), "%d", pct);
  this->send_command("display", "set_brightness", value);
}

// Toggle display
void UlanziBridge::toggle_display() {
  this->send_command("display", "toggle_on");
}

// Send notification to a button
void UlanziBridge::send_notification(int button_id, int page_id, const char *message) {
  char btn_id_str[8];
  char page_id_str[8];
  snprintf(btn_id_str, sizeof(btn_id_str), "%d", button_id);
  snprintf(page_id_str, sizeof(page_id_str), "%d", page_id);

  ESP_LOGD(TAG, "Sending notification to button %d (page %d): %s", button_id, page_id, message);
  this->send_command("button", "add_notification", btn_id_str, page_id_str, message);
}

// Update backlight light state from D200 brightness event
void UlanziBridge::update_backlight_brightness(int pct) {
  if (this->backlight_light_state_ == nullptr) return;

  float brightness = (float)pct / 100.0f;
  if (brightness < 0.0f) brightness = 0.0f;
  if (brightness > 1.0f) brightness = 1.0f;

  auto call = this->backlight_light_state_->make_call();
  call.set_state(brightness > 0.0f);
  if (brightness > 0.0f) {
    call.set_brightness(brightness);
  }
  call.perform();

  // Brightness synced from D200
}

#ifdef USE_TIME
// Sync system time to D200
void UlanziBridge::sync_time(time::RealTimeClock *time_source) {
  if (time_source == nullptr) return;
  auto now = time_source->now();
  if (!now.is_valid()) {
    ESP_LOGW(TAG, "Time source not ready, skipping sync");
    return;
  }
  char buf[20];
  snprintf(buf, sizeof(buf), "%04d-%02d-%02d %02d:%02d:%02d",
           now.year, now.month, now.day_of_month,
           now.hour, now.minute, now.second);
  ESP_LOGD(TAG, "Syncing time to D200: %s", buf);
  this->send_command("system", "set_time", buf);
}
#endif

// Send sensor reading
void UlanziBridge::send_sensor_reading(const char *sensor, float value) {
  char value_str[16];
  snprintf(value_str, sizeof(value_str), "%.2f", value);
  this->send_command("sensor", "update", sensor, value_str);
}

// Configure a button (legacy, kept for compatibility)
void UlanziBridge::configure_button(int button_id, const char *json_config) {
  ESP_LOGD(TAG, "Configuring button %d", button_id);
  char id_str[4];
  snprintf(id_str, sizeof(id_str), "%d", button_id);
  this->send_command("button", "set_config", id_str, json_config, "false");
}

// Push a sensor value to a button's graph buffer
void UlanziBridge::push_button_value(int button_id, int page_id, float value) {
  char id_str[4];
  snprintf(id_str, sizeof(id_str), "%d", button_id);
  char value_str[16];
  snprintf(value_str, sizeof(value_str), "%.4f", value);
  char page_str[8];
  snprintf(page_str, sizeof(page_str), "%d", page_id);
  this->send_command("button", "push_value", id_str, value_str, page_str);
}

// Dynamic button display updates
void UlanziBridge::update_button_display(int button_id, int page_id,
                                         const char *text,
                                         const char *text2,
                                         const char *icon_id,
                                         const char *color,
                                         const char *text_color,
                                         const char *text2_color,
                                         const char *icon_color,
                                         const char *style,
                                         const char *graph_color,
                                         const char *value) {
  std::string json = "{";
  bool first = true;

  auto add_field = [&](const char *key, const char *value) {
    if (value != nullptr && value[0] != '\0') {
      if (!first) json += ",";
      json += "\"";
      json += key;
      json += "\":\"";
      for (const char *p = value; *p; p++) {
        if (*p == '"') {
          json += "\\\"";
        } else if (*p == '\\') {
          json += "\\\\";
        } else {
          json += *p;
        }
      }
      json += "\"";
      first = false;
    }
  };

  add_field("text", text);
  add_field("text2", text2);
  add_field("icon_id", icon_id);
  add_field("color", color);
  add_field("text_color", text_color);
  add_field("text2_color", text2_color);
  add_field("icon_color", icon_color);
  add_field("style", style);
  add_field("graph_color", graph_color);
  add_field("value", value);

  json += "}";

  char id_str[4];
  snprintf(id_str, sizeof(id_str), "%d", button_id);
  char page_str[8];
  snprintf(page_str, sizeof(page_str), "%d", page_id);
  this->send_command("button", "update_display", id_str, json.c_str(), page_str);

  // update_display sent for button
}

// --- Section configuration methods (called from codegen) ---

void UlanziBridge::add_section(int position, int page) {
  SectionConfig section;
  section.position = position;
  section.page = page;
  this->sections_.push_back(section);
}

void UlanziBridge::set_section_pressed_style(int section_index, const std::string &property, const std::string &value) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &ps = this->sections_[section_index].pressed_style;
  if (property == "color") { ps.color = value; ps.has_color = true; }
  else if (property == "text_color") { ps.text_color = value; ps.has_text_color = true; }
  else if (property == "text2_color") { ps.text2_color = value; ps.has_text2_color = true; }
  else if (property == "icon_color") { ps.icon_color = value; ps.has_icon_color = true; }
  else if (property == "style") { ps.style = value; ps.has_style = true; }
}

void UlanziBridge::add_layout(int section_index, const std::string &widget_type) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  LayoutConfig layout;
  layout.widget_type = widget_type;
  this->sections_[section_index].layouts.push_back(layout);
}

void UlanziBridge::set_layout_condition(int section_index, int layout_index, ConditionFunction func) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &layouts = this->sections_[section_index].layouts;
  if (layout_index < 0 || layout_index >= (int)layouts.size()) return;
  layouts[layout_index].when = func;
}

static WidgetProperty* find_widget_property(LayoutConfig &layout, const std::string &property) {
  if (property == "color") return &layout.color;
  if (property == "text") return &layout.text;
  if (property == "text_color") return &layout.text_color;
  if (property == "text2") return &layout.text2;
  if (property == "text2_color") return &layout.text2_color;
  if (property == "icon_id") return &layout.icon_id;
  if (property == "icon_color") return &layout.icon_color;
  if (property == "style") return &layout.style;
  if (property == "graph_color") return &layout.graph_color;
  if (property == "value") return &layout.value;
  return nullptr;
}

void UlanziBridge::set_layout_property_static(int section_index, int layout_index,
                                               const std::string &property, const std::string &value) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &layouts = this->sections_[section_index].layouts;
  if (layout_index < 0 || layout_index >= (int)layouts.size()) return;

  WidgetProperty *prop = find_widget_property(layouts[layout_index], property);
  if (prop) {
    prop->static_value = value;
    prop->is_template = false;
  }
}

void UlanziBridge::set_layout_property_template(int section_index, int layout_index,
                                                  const std::string &property, TemplateFunction func) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &layouts = this->sections_[section_index].layouts;
  if (layout_index < 0 || layout_index >= (int)layouts.size()) return;

  WidgetProperty *prop = find_widget_property(layouts[layout_index], property);
  if (prop) {
    prop->template_func = func;
    prop->is_template = true;
  }
}

void UlanziBridge::set_layout_graph_params(int section_index, int layout_index, float min, float max, int size) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &layouts = this->sections_[section_index].layouts;
  if (layout_index < 0 || layout_index >= (int)layouts.size()) return;
  layouts[layout_index].min = min;
  layouts[layout_index].max = max;
  layouts[layout_index].size = size;
}

void UlanziBridge::add_layout_status_item(int section_index, int layout_index, const std::string &item) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &layouts = this->sections_[section_index].layouts;
  if (layout_index < 0 || layout_index >= (int)layouts.size()) return;
  layouts[layout_index].status_items.push_back(item);
}

void UlanziBridge::add_layout_chip(int section_index, int layout_index,
                                    const std::string &icon_id, const std::string &label, const std::string &bg_color) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  auto &layouts = this->sections_[section_index].layouts;
  if (layout_index < 0 || layout_index >= (int)layouts.size()) return;
  ChipDef chip;
  chip.icon_id = icon_id;
  chip.label = label;
  chip.bg_color = bg_color;
  layouts[layout_index].chips.push_back(chip);
}

void UlanziBridge::add_notification_service(const std::string &name, int button_id, int page_id) {
  NotificationServiceDef def;
  def.name = name;
  def.button_id = button_id;
  def.page_id = page_id;
  this->notification_services_.push_back(def);
}

void UlanziBridge::register_section_press_trigger(int section_index, SectionEventTrigger *trigger) {
  this->section_press_triggers_[section_index].push_back(trigger);
}

void UlanziBridge::register_section_release_trigger(int section_index, SectionEventTrigger *trigger) {
  this->section_release_triggers_[section_index].push_back(trigger);
}

void UlanziBridge::set_section_layout(int position, int layout_index) {
  int si = this->find_section_for_position_(position);
  if (si < 0) return;
  if (layout_index < 0 || layout_index >= (int)this->sections_[si].layouts.size()) return;
  this->active_layout_index_[si] = layout_index;
  this->last_template_values_.erase(si);
  this->send_section_config_(si);
}

int UlanziBridge::get_active_layout(int position) const {
  int si = this->find_section_for_position_(position);
  if (si < 0) return -1;
  auto it = this->active_layout_index_.find(si);
  if (it != this->active_layout_index_.end()) return it->second;
  return 0;
}

// --- Core section/layout logic ---

int UlanziBridge::evaluate_active_layout_(int section_index) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return 0;
  const auto &layouts = this->sections_[section_index].layouts;

  for (int i = 0; i < (int)layouts.size(); i++) {
    if (layouts[i].when == nullptr) {
      return i; // No condition = always matches
    }
    if (layouts[i].when()) {
      return i;
    }
  }

  // No layout matched - use last one as fallback
  return layouts.empty() ? 0 : (int)layouts.size() - 1;
}

void UlanziBridge::evaluate_and_update_sections_() {
  for (int i = 0; i < (int)this->sections_.size(); i++) {
    const auto &section = this->sections_[i];

    // Skip sections not on the current page
    if (!this->is_section_on_page_(section, this->current_page_)) {
      continue;
    }

    // Evaluate active layout
    int new_layout = this->evaluate_active_layout_(i);
    auto it = this->active_layout_index_.find(i);
    int old_layout = (it != this->active_layout_index_.end()) ? it->second : -1;

    if (new_layout != old_layout) {
      // Layout changed: full config re-send
      ESP_LOGD(TAG, "Section %d (pos %d) layout changed: %d -> %d", i, section.position, old_layout, new_layout);
      this->active_layout_index_[i] = new_layout;
      this->last_template_values_.erase(i);
      this->last_pushed_graph_value_.erase(i);
      this->send_section_config_(i);

      // Push initial graph data point for line_graph/gauge widgets
      if (new_layout >= 0 && new_layout < (int)section.layouts.size()) {
        const auto &new_ly = section.layouts[new_layout];
        if (new_ly.value.is_template &&
            (new_ly.widget_type == "line_graph" || new_ly.widget_type == "gauge")) {
          std::string val_str = new_ly.value.get_value();
          if (!val_str.empty()) {
            float val = strtof(val_str.c_str(), nullptr);
            this->last_pushed_graph_value_[i] = val;
            int page = (section.page == -1) ? this->current_page_ : section.page;
            this->push_button_value(section.position, page, val);
          }
        }
      }
    } else {
      // Same layout: check template properties for changes
      if (new_layout < 0 || new_layout >= (int)section.layouts.size()) continue;
      const auto &layout = section.layouts[new_layout];

      std::map<std::string, std::string> changed_props;
      auto check_property = [&](const WidgetProperty &prop, const std::string &prop_name) {
        if (prop.is_template) {
          std::string current_value = prop.get_value();
          auto cache_it = this->last_template_values_.find(i);
          if (cache_it == this->last_template_values_.end() ||
              cache_it->second[prop_name] != current_value) {
            changed_props[prop_name] = current_value;
            this->last_template_values_[i][prop_name] = current_value;
          }
        }
      };

      check_property(layout.color, "color");
      check_property(layout.text, "text");
      check_property(layout.text_color, "text_color");
      check_property(layout.text2, "text2");
      check_property(layout.text2_color, "text2_color");
      check_property(layout.icon_id, "icon_id");
      check_property(layout.icon_color, "icon_color");
      check_property(layout.style, "style");
      check_property(layout.graph_color, "graph_color");
      check_property(layout.value, "value");

      if (!changed_props.empty()) {
        ESP_LOGD(TAG, "Template values changed for section %d (pos %d), sending partial update (%d props)",
                 i, section.position, changed_props.size());
        this->send_section_display_update_(i, changed_props);
      }

      // Push graph data points only when value actually changed
      if (layout.value.is_template &&
          (layout.widget_type == "line_graph" || layout.widget_type == "gauge")) {
        std::string val_str = layout.value.get_value();
        if (!val_str.empty()) {
          float val = strtof(val_str.c_str(), nullptr);
          auto prev_it = this->last_pushed_graph_value_.find(i);
          if (prev_it == this->last_pushed_graph_value_.end() ||
              std::abs(prev_it->second - val) > 0.001f) {
            this->last_pushed_graph_value_[i] = val;
            int page = (section.page == -1) ? this->current_page_ : section.page;
            this->push_button_value(section.position, page, val);
          }
        }
      }
    }
  }
}

std::string UlanziBridge::build_section_json_config_(int section_index) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return "{}";
  const auto &section = this->sections_[section_index];

  int layout_idx = 0;
  auto it = this->active_layout_index_.find(section_index);
  if (it != this->active_layout_index_.end()) layout_idx = it->second;
  if (layout_idx < 0 || layout_idx >= (int)section.layouts.size()) return "{}";

  const auto &layout = section.layouts[layout_idx];

  // Evaluate all properties
  std::string color = layout.color.get_value();
  std::string text = layout.text.get_value();
  std::string text_color = layout.text_color.get_value();
  std::string text2 = layout.text2.get_value();
  std::string text2_color = layout.text2_color.get_value();
  std::string icon_id = layout.icon_id.get_value();
  std::string icon_color = layout.icon_color.get_value();
  std::string style_val = layout.style.get_value();
  std::string graph_color = layout.graph_color.get_value();
  std::string value = layout.value.get_value();

  // Helper to JSON-escape a string value
  auto json_escape = [](const std::string &s) -> std::string {
    std::string out;
    for (char c : s) {
      if (c == '"') out += "\\\"";
      else if (c == '\\') out += "\\\\";
      else out += c;
    }
    return out;
  };

  // Build default state JSON
  auto build_state = [&](const std::string &c, const std::string &t, const std::string &tc,
                         const std::string &t2, const std::string &t2c, const std::string &iid,
                         const std::string &ic, const std::string &st, const std::string &gc,
                         const std::string &v) -> std::string {
    std::string s = "{";
    s += "\"color\":\"" + json_escape(c) + "\"";
    s += ",\"text\":\"" + json_escape(t) + "\"";
    s += ",\"text_color\":\"" + json_escape(tc) + "\"";
    s += ",\"text2\":\"" + json_escape(t2) + "\"";
    s += ",\"text2_color\":\"" + json_escape(t2c) + "\"";
    s += ",\"icon_id\":\"" + json_escape(iid) + "\"";
    s += ",\"icon_color\":\"" + json_escape(ic) + "\"";
    s += ",\"style\":\"" + json_escape(st) + "\"";
    if (!gc.empty()) s += ",\"graph_color\":\"" + json_escape(gc) + "\"";
    if (!v.empty()) s += ",\"value\":\"" + json_escape(v) + "\"";
    s += "}";
    return s;
  };

  std::string default_state = build_state(color, text, text_color, text2, text2_color,
                                           icon_id, icon_color, style_val, graph_color, value);

  // Build pressed state: merge active layout props with pressed_style overrides
  const auto &ps = section.pressed_style;
  std::string pressed_color = ps.has_color ? ps.color : color;
  std::string pressed_text_color = ps.has_text_color ? ps.text_color : text_color;
  std::string pressed_text2_color = ps.has_text2_color ? ps.text2_color : text2_color;
  std::string pressed_icon_color = ps.has_icon_color ? ps.icon_color : icon_color;
  std::string pressed_style = ps.has_style ? ps.style : style_val;

  std::string pressed_state = build_state(pressed_color, text, pressed_text_color,
                                           text2, pressed_text2_color, icon_id,
                                           pressed_icon_color, pressed_style, graph_color, value);

  // Build the full ButtonConfig JSON for D200
  std::string json = "{";
  json += "\"id\":" + std::to_string(section.position);
  json += ",\"label\":\"\"";
  json += ",\"type\":\"momentary\"";
  json += ",\"current_state\":\"default\"";
  json += ",\"states\":{\"default\":" + default_state + ",\"pressed\":" + pressed_state + "}";

  // Map widget_type to D200 wide_layout
  if (layout.widget_type == "gauge") {
    json += ",\"wide_layout\":\"gauge\"";
    char buf[64];
    snprintf(buf, sizeof(buf), ",\"graph_min\":%.2f,\"graph_max\":%.2f,\"graph_size\":%d",
             layout.min, layout.max, layout.size);
    json += buf;
  } else if (layout.widget_type == "line_graph") {
    json += ",\"wide_layout\":\"line-graph\"";
    char buf[64];
    snprintf(buf, sizeof(buf), ",\"graph_min\":%.2f,\"graph_max\":%.2f,\"graph_size\":%d",
             layout.min, layout.max, layout.size);
    json += buf;
  } else if (layout.widget_type == "status") {
    json += ",\"wide_layout\":\"status\"";
    if (!layout.status_items.empty()) {
      json += ",\"status_items\":[";
      for (int j = 0; j < (int)layout.status_items.size(); j++) {
        if (j > 0) json += ",";
        json += "\"" + json_escape(layout.status_items[j]) + "\"";
      }
      json += "]";
    }
  } else if (layout.widget_type == "chips") {
    json += ",\"wide_layout\":\"chips\"";
  } else if (layout.widget_type == "notification") {
    json += ",\"wide_layout\":\"notification\"";
  }
  // entity_view = no wide_layout (default)

  json += "}";
  return json;
}

void UlanziBridge::send_section_config_(int section_index) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  const auto &section = this->sections_[section_index];

  std::string json = this->build_section_json_config_(section_index);
  this->configure_button_on_page(section.position, section.page, json.c_str());
}

void UlanziBridge::send_section_display_update_(int section_index, const std::map<std::string, std::string> &props) {
  if (section_index < 0 || section_index >= (int)this->sections_.size()) return;
  const auto &section = this->sections_[section_index];

  // Build minimal JSON with only changed properties
  std::string json = "{";
  bool first = true;
  for (const auto &kv : props) {
    if (!first) json += ",";
    json += "\"";
    json += kv.first;
    json += "\":\"";
    // Escape value
    for (char c : kv.second) {
      if (c == '"') json += "\\\"";
      else if (c == '\\') json += "\\\\";
      else json += c;
    }
    json += "\"";
    first = false;
  }
  json += "}";

  char id_str[8];
  snprintf(id_str, sizeof(id_str), "%d", section.position);
  char page_str[8];
  snprintf(page_str, sizeof(page_str), "%d", section.page);

  this->send_command("button", "update_display", id_str, json.c_str(), page_str);
}

void UlanziBridge::send_all_section_configs_() {
  ESP_LOGI(TAG, "Sending all section configs (%d sections)", this->sections_.size());

  // Two passes: page-specific sections first (to auto-create pages on daemon),
  // then "all pages" sections (page=-1) so they get distributed to all pages.
  auto send_config = [this](int i) {
    int layout_idx = this->evaluate_active_layout_(i);
    this->active_layout_index_[i] = layout_idx;
    this->last_template_values_.erase(i);
    this->send_section_config_(i);
  };

  // Pass 1: page-specific sections (page >= 0)
  for (int i = 0; i < (int)this->sections_.size(); i++) {
    if (this->sections_[i].page >= 0) send_config(i);
  }
  // Pass 2: all-pages sections (page < 0)
  for (int i = 0; i < (int)this->sections_.size(); i++) {
    if (this->sections_[i].page < 0) send_config(i);
  }
}

void UlanziBridge::handle_ready_(int page, int page_count) {
  ESP_LOGI(TAG, "D200 ready! page=%d, page_count=%d", page, page_count);
  this->current_page_ = page;
  this->page_count_ = page_count;

  // Mark as connected and fire callbacks
  if (!this->connected_) {
    this->connected_ = true;
    this->on_connected_callbacks_.call();
  }

  // Clear old buttons then send current configs
  this->reset_all_buttons();
  this->send_all_section_configs_();
  this->configs_sent_ = true;
}

}  // namespace ulanzi_d200
}  // namespace esphome
