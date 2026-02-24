#pragma once

#include "esphome/core/component.h"
#include "esphome/core/automation.h"
#include "esphome/components/uart/uart.h"
#include "esphome/components/light/light_output.h"
#include <map>
#include <vector>
#include <functional>

namespace esphome {
namespace time { class RealTimeClock; }
namespace ulanzi_d200 {

class UlanziBridge;

// Light output for display backlight (brightness-only, no color)
class UlanziBacklightLight : public light::LightOutput {
 public:
  UlanziBacklightLight(UlanziBridge *parent) : parent_(parent) {}
  light::LightTraits get_traits() override {
    auto traits = light::LightTraits();
    traits.set_supported_color_modes({light::ColorMode::BRIGHTNESS});
    return traits;
  }
  void write_state(light::LightState *state) override;

 protected:
  UlanziBridge *parent_;
};

// Triggers
class ConnectedTrigger : public Trigger<> {
 public:
  explicit ConnectedTrigger(UlanziBridge *parent);
};

class DisconnectedTrigger : public Trigger<> {
 public:
  explicit DisconnectedTrigger(UlanziBridge *parent);
};

// Component-level section triggers: fire with position (int)
class SectionPressTrigger : public Trigger<int> {
 public:
  explicit SectionPressTrigger(UlanziBridge *parent);
};

class SectionReleaseTrigger : public Trigger<int> {
 public:
  explicit SectionReleaseTrigger(UlanziBridge *parent);
};

// Per-section event trigger: fires with no params
class SectionEventTrigger : public Trigger<> {
 public:
  SectionEventTrigger() = default;
};

// Page change trigger: fires with (page_id, page_count)
class PageChangeTrigger : public Trigger<int, int> {
 public:
  explicit PageChangeTrigger(UlanziBridge *parent);
};

// Mode change trigger: fires with (mode)
class ModeChangeTrigger : public Trigger<std::string> {
 public:
  explicit ModeChangeTrigger(UlanziBridge *parent);
};

// Template function for widget properties
using TemplateFunction = std::function<optional<std::string>()>;

// Condition function for layout selection
using ConditionFunction = std::function<bool()>;

// Widget property - can be static string or template lambda
struct WidgetProperty {
  std::string static_value;
  TemplateFunction template_func{nullptr};
  bool is_template{false};

  std::string get_value() const {
    if (is_template && template_func) {
      auto result = template_func();
      return result.has_value() ? result.value() : static_value;
    }
    return static_value;
  }
};

// Chip definition for chips widget
struct ChipDef {
  std::string icon_id;
  std::string label;
  std::string bg_color;
};

// Pressed style overrides (applied on top of active layout while pressed)
struct PressedStyle {
  std::string color, text_color, text2_color, icon_color, style;
  bool has_color{false}, has_text_color{false}, has_text2_color{false},
       has_icon_color{false}, has_style{false};
};

// Layout configuration - one visual state within a section
struct LayoutConfig {
  std::string widget_type;         // entity_view, gauge, line_graph, status, chips
  ConditionFunction when{nullptr}; // nullptr = always matches
  WidgetProperty color, text, text_color, text2, text2_color,
                 icon_id, icon_color, style, graph_color, value;
  float min{0}, max{100};
  int size{20};
  std::vector<std::string> status_items;
  std::vector<ChipDef> chips;
};

// Notification service definition (for auto-registration with HA API)
struct NotificationServiceDef {
  std::string name;
  int button_id;
  int page_id;
};

// Section configuration - a physical display area
struct SectionConfig {
  int position;    // 0-13
  int page{-1};    // -1 = all pages
  PressedStyle pressed_style;
  std::vector<LayoutConfig> layouts;
};

// Main Component
class UlanziBridge : public Component, public uart::UARTDevice {
 public:
  void setup() override;
  void loop() override;
  void dump_config() override;
  float get_setup_priority() const override { return setup_priority::LATE; }

  // Public API - JSON IPC Format
  void send_command(const char *module, const char *action, const char *param1 = nullptr, const char *param2 = nullptr, const char *param3 = nullptr);
  void send_ping();
  void reset_all_buttons();
  void set_brightness(uint8_t brightness);
  void toggle_display();
  void send_sensor_reading(const char *sensor, float value);
  void send_notification(int button_id, int page_id, const char *message);
#ifdef USE_TIME
  void sync_time(time::RealTimeClock *time_source);
#endif

  // Backlight light entity
  void set_backlight_light(light::LightState *state) { this->backlight_light_state_ = state; }
  void update_backlight_brightness(int pct);

  // Section configuration (called from codegen)
  void add_section(int position, int page);
  void set_section_pressed_style(int section_index, const std::string &property, const std::string &value);
  void add_layout(int section_index, const std::string &widget_type);
  void set_layout_condition(int section_index, int layout_index, ConditionFunction func);
  void set_layout_property_static(int section_index, int layout_index,
                                  const std::string &property, const std::string &value);
  void set_layout_property_template(int section_index, int layout_index,
                                    const std::string &property, TemplateFunction func);
  void set_layout_graph_params(int section_index, int layout_index, float min, float max, int size);
  void add_layout_status_item(int section_index, int layout_index, const std::string &item);
  void add_layout_chip(int section_index, int layout_index,
                       const std::string &icon_id, const std::string &label, const std::string &bg_color);

  // Notification service auto-registration
  void add_notification_service(const std::string &name, int button_id, int page_id);

  // Section trigger registration
  void register_section_press_trigger(int section_index, SectionEventTrigger *trigger);
  void register_section_release_trigger(int section_index, SectionEventTrigger *trigger);

  // Programmatic layout control
  void set_section_layout(int position, int layout_index);
  int get_active_layout(int position) const;

  // Legacy API kept for compatibility (configure_button, update_button_display, etc.)
  void configure_button(int button_id, const char *json_config);
  void configure_button_on_page(int button_id, int page_id, const char *json_config);
  void update_button_display(int button_id, int page_id,
                            const char *text = nullptr,
                            const char *text2 = nullptr,
                            const char *icon_id = nullptr,
                            const char *color = nullptr,
                            const char *text_color = nullptr,
                            const char *text2_color = nullptr,
                            const char *icon_color = nullptr,
                            const char *style = nullptr,
                            const char *graph_color = nullptr,
                            const char *value = nullptr);
  void push_button_value(int button_id, int page_id, float value);

  // Page management
  void navigate_page_next();
  void navigate_page_prev();
  void navigate_page_jump(int target_page);
  int get_current_page() const { return current_page_; }
  int get_page_count() const { return page_count_; }
  const std::string &get_current_page_label() const { return page_label_; }

  // Mode management
  void set_mode(const std::string &mode);
  const std::string &get_current_mode() const { return current_mode_; }

  bool is_connected() const { return connected_; }

  // Callbacks
  void add_on_connected_callback(std::function<void()> &&callback) {
    on_connected_callbacks_.add(std::move(callback));
  }
  void add_on_disconnected_callback(std::function<void()> &&callback) {
    on_disconnected_callbacks_.add(std::move(callback));
  }
  void add_on_section_press_callback(std::function<void(int)> &&callback) {
    on_section_press_callbacks_.add(std::move(callback));
  }
  void add_on_section_release_callback(std::function<void(int)> &&callback) {
    on_section_release_callbacks_.add(std::move(callback));
  }
  void add_on_page_change_callback(std::function<void(int, int)> &&callback) {
    on_page_change_callbacks_.add(std::move(callback));
  }
  void add_on_mode_change_callback(std::function<void(const std::string &)> &&callback) {
    on_mode_change_callbacks_.add(std::move(callback));
  }

 protected:
  // UART Response Processing
  void process_uart_data_();
  void parse_response_line_(const std::string &line);
  void parse_json_event_(const std::string &json);
  void check_connection_timeout_();

  // Section/layout evaluation
  int evaluate_active_layout_(int section_index);
  void evaluate_and_update_sections_();
  std::string build_section_json_config_(int section_index);
  void send_section_config_(int section_index);
  void send_section_display_update_(int section_index, const std::map<std::string, std::string> &props);
  void send_all_section_configs_();
  void send_sections_for_page_(int page);
  int find_section_for_position_(int position) const;
  bool is_section_on_page_(const SectionConfig &section, int page) const;

  // Page management helpers
  void handle_page_change_(int page, int page_count, const std::string &label = "");
  void handle_ready_(int page, int page_count);

  // State
  bool connected_ = false;
  uint32_t last_message_time_ = 0;
  uint32_t message_counter_ = 0;
  std::string rx_buffer_;
  std::vector<SectionConfig> sections_;
  std::vector<NotificationServiceDef> notification_services_;
  std::map<int, int> active_layout_index_;  // section_index -> active layout index
  std::map<int, std::map<std::string, std::string>> last_template_values_;  // Cache for template results
  std::map<int, float> last_pushed_graph_value_;  // Cache for graph dedup

  // Per-section event triggers
  std::map<int, std::vector<SectionEventTrigger*>> section_press_triggers_;
  std::map<int, std::vector<SectionEventTrigger*>> section_release_triggers_;

  // Backlight light
  light::LightState *backlight_light_state_{nullptr};
  int last_brightness_sent_{-1};

  // Batch command queue (serialized: one batch in flight at a time)
  std::vector<std::string> pending_batch_;
  void flush_batch_();
  bool batch_in_flight_{false};
  uint32_t batch_sent_time_{0};
  static constexpr uint32_t BATCH_TIMEOUT_MS = 3000;  // Max wait for batch response
  static constexpr int MAX_BATCH_COMMANDS = 4;  // Max commands per batch (keeps payload < 4KB TTY buffer)

  // Periodic ping for health monitoring
  uint32_t last_ping_time_{0};
  static constexpr uint32_t PING_INTERVAL_MS = 30000;  // Ping every 30s

  // Guard against duplicate send_all_section_configs_
  bool configs_sent_{false};

  // Page state
  int current_page_ = 0;
  int page_count_ = 1;
  std::string page_label_;

  // Mode state
  std::string current_mode_ = "run";

  // Callbacks
  CallbackManager<void()> on_connected_callbacks_;
  CallbackManager<void()> on_disconnected_callbacks_;
  CallbackManager<void(int)> on_section_press_callbacks_;
  CallbackManager<void(int)> on_section_release_callbacks_;
  CallbackManager<void(int, int)> on_page_change_callbacks_;
  CallbackManager<void(const std::string &)> on_mode_change_callbacks_;

  static constexpr uint32_t CONNECTION_TIMEOUT_MS = 10000;
  static constexpr size_t MAX_BUFFER_SIZE = 4096;
};

}  // namespace ulanzi_d200
}  // namespace esphome
