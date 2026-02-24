"""
Ulanzi D200 Serial Bridge Component for ESPHome
Sections/Layouts/Widgets architecture.
"""

import esphome.codegen as cg
import esphome.config_validation as cv
from esphome import automation
from esphome.components import uart, light
from esphome.const import CONF_ID, CONF_OUTPUT_ID

DEPENDENCIES = ['uart']
AUTO_LOAD = ['json', 'light']

ulanzi_d200_ns = cg.esphome_ns.namespace('ulanzi_d200')
UlanziBridge = ulanzi_d200_ns.class_('UlanziBridge', cg.Component, uart.UARTDevice)
UlanziBacklightLight = ulanzi_d200_ns.class_('UlanziBacklightLight', light.LightOutput)

# Triggers
ConnectedTrigger = ulanzi_d200_ns.class_('ConnectedTrigger', automation.Trigger.template())
DisconnectedTrigger = ulanzi_d200_ns.class_('DisconnectedTrigger', automation.Trigger.template())
SectionPressTrigger = ulanzi_d200_ns.class_('SectionPressTrigger', automation.Trigger.template(cg.int_))
SectionReleaseTrigger = ulanzi_d200_ns.class_('SectionReleaseTrigger', automation.Trigger.template(cg.int_))
SectionEventTrigger = ulanzi_d200_ns.class_('SectionEventTrigger', automation.Trigger.template())
PageChangeTrigger = ulanzi_d200_ns.class_('PageChangeTrigger', automation.Trigger.template(cg.int_, cg.int_))
ModeChangeTrigger = ulanzi_d200_ns.class_('ModeChangeTrigger', automation.Trigger.template(cg.std_string))

# Config keys
CONF_ON_CONNECTED = 'on_connected'
CONF_ON_DISCONNECTED = 'on_disconnected'
CONF_ON_SECTION_PRESS = 'on_section_press'
CONF_ON_SECTION_RELEASE = 'on_section_release'
CONF_ON_PAGE_CHANGE = 'on_page_change'
CONF_ON_MODE_CHANGE = 'on_mode_change'
CONF_ON_PRESS = 'on_press'
CONF_ON_RELEASE = 'on_release'
CONF_UART_ID = 'uart_id'
CONF_SECTIONS = 'sections'
CONF_POSITION = 'position'
CONF_PAGE = 'page'
CONF_PRESSED_STYLE = 'pressed_style'
CONF_LAYOUTS = 'layouts'
CONF_WIDGET = 'widget'
CONF_WHEN = 'when'
CONF_ICON_ID = 'icon_id'
CONF_COLOR = 'color'
CONF_TEXT = 'text'
CONF_TEXT_COLOR = 'text_color'
CONF_TEXT2 = 'text2'
CONF_TEXT2_COLOR = 'text2_color'
CONF_ICON_COLOR = 'icon_color'
CONF_STYLE = 'style'
CONF_GRAPH_COLOR = 'graph_color'
CONF_VALUE = 'value'
CONF_MIN = 'min'
CONF_MAX = 'max'
CONF_SIZE = 'size'
CONF_ITEMS = 'items'
CONF_CHIPS = 'chips'
CONF_LABEL = 'label'
CONF_BG_COLOR = 'bg_color'
CONF_BACKLIGHT_OUTPUT_ID = 'backlight_output_id'
CONF_BACKLIGHT_LIGHT_ID = 'backlight_light_id'
CONF_SERVICE_ID = 'service_id'


# Helper to accept either string or lambda
def string_or_lambda(value):
    if isinstance(value, cv.Lambda):
        return value
    return cv.string(value)


CHIP_SCHEMA = cv.Schema({
    cv.Optional(CONF_ICON_ID, default=''): cv.string,
    cv.Optional(CONF_LABEL, default=''): cv.string,
    cv.Optional(CONF_BG_COLOR, default=''): cv.string,
})

PRESSED_STYLE_SCHEMA = cv.Schema({
    cv.Optional(CONF_COLOR): cv.string,
    cv.Optional(CONF_TEXT_COLOR): cv.string,
    cv.Optional(CONF_TEXT2_COLOR): cv.string,
    cv.Optional(CONF_ICON_COLOR): cv.string,
    cv.Optional(CONF_STYLE): cv.string,
})

LAYOUT_SCHEMA = cv.Schema({
    cv.Required(CONF_WIDGET): cv.one_of(
        'entity_view', 'gauge', 'line_graph', 'status', 'chips', 'notification', lower=True
    ),
    cv.Optional(CONF_WHEN): cv.lambda_,
    # Common widget properties (string or lambda)
    cv.Optional(CONF_COLOR, default='#333333'): string_or_lambda,
    cv.Optional(CONF_TEXT, default=''): string_or_lambda,
    cv.Optional(CONF_TEXT_COLOR, default='#FFFFFF'): string_or_lambda,
    cv.Optional(CONF_TEXT2, default=''): string_or_lambda,
    cv.Optional(CONF_TEXT2_COLOR, default='#FFFFFF'): string_or_lambda,
    cv.Optional(CONF_ICON_ID, default=''): string_or_lambda,
    cv.Optional(CONF_ICON_COLOR, default='#FFFFFF'): string_or_lambda,
    cv.Optional(CONF_STYLE, default='constant'): string_or_lambda,
    cv.Optional(CONF_GRAPH_COLOR, default=''): string_or_lambda,
    cv.Optional(CONF_VALUE, default=''): string_or_lambda,
    # Widget-specific params
    cv.Optional(CONF_MIN, default=0.0): cv.float_,
    cv.Optional(CONF_MAX, default=100.0): cv.float_,
    cv.Optional(CONF_SIZE, default=20): cv.int_range(min=2, max=100),
    cv.Optional(CONF_ITEMS): cv.ensure_list(cv.string),
    cv.Optional(CONF_CHIPS): cv.ensure_list(CHIP_SCHEMA),
    cv.Optional(CONF_SERVICE_ID): cv.valid_name,
})

SECTION_SCHEMA = cv.Schema({
    cv.Required(CONF_POSITION): cv.int_range(min=0, max=13),
    cv.Optional(CONF_PAGE, default=-1): cv.int_,
    cv.Optional(CONF_ON_PRESS): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(SectionEventTrigger),
    }),
    cv.Optional(CONF_ON_RELEASE): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(SectionEventTrigger),
    }),
    cv.Optional(CONF_PRESSED_STYLE, default={}): PRESSED_STYLE_SCHEMA,
    cv.Required(CONF_LAYOUTS): cv.ensure_list(LAYOUT_SCHEMA),
})

CONFIG_SCHEMA = cv.Schema({
    cv.GenerateID(): cv.declare_id(UlanziBridge),
    cv.GenerateID(CONF_UART_ID): cv.use_id(uart.UARTComponent),
    cv.GenerateID(CONF_BACKLIGHT_OUTPUT_ID): cv.declare_id(UlanziBacklightLight),
    cv.GenerateID(CONF_BACKLIGHT_LIGHT_ID): cv.declare_id(light.LightState),
    cv.Optional(CONF_ON_CONNECTED): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(ConnectedTrigger),
    }),
    cv.Optional(CONF_ON_DISCONNECTED): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(DisconnectedTrigger),
    }),
    cv.Optional(CONF_ON_SECTION_PRESS): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(SectionPressTrigger),
    }),
    cv.Optional(CONF_ON_SECTION_RELEASE): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(SectionReleaseTrigger),
    }),
    cv.Optional(CONF_ON_PAGE_CHANGE): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(PageChangeTrigger),
    }),
    cv.Optional(CONF_ON_MODE_CHANGE): automation.validate_automation({
        cv.GenerateID(automation.CONF_TRIGGER_ID): cv.declare_id(ModeChangeTrigger),
    }),
    cv.Optional(CONF_SECTIONS, default=[]): cv.ensure_list(SECTION_SCHEMA),
}).extend(cv.COMPONENT_SCHEMA)


async def to_code(config):
    var = cg.new_Pvariable(config[CONF_ID])
    await cg.register_component(var, config)
    await uart.register_uart_device(var, config)

    # Create backlight light entity via proper ESPHome registration
    backlight_output = cg.new_Pvariable(config[CONF_BACKLIGHT_OUTPUT_ID], var)
    backlight_light = cg.new_Pvariable(config[CONF_BACKLIGHT_LIGHT_ID], backlight_output)
    cg.add(backlight_light.set_name("Display Backlight"))
    cg.add(backlight_light.set_object_id("display_backlight"))
    cg.add(backlight_light.set_default_transition_length(0))
    cg.add(backlight_light.set_restore_mode(light.LightRestoreMode.LIGHT_RESTORE_DEFAULT_ON))
    await cg.register_component(backlight_light, {})
    cg.add(cg.App.register_light(backlight_light))
    cg.add(var.set_backlight_light(backlight_light))

    # Process sections
    for section_index, section_conf in enumerate(config.get(CONF_SECTIONS, [])):
        position = section_conf[CONF_POSITION]
        page = section_conf[CONF_PAGE]

        # Add section
        cg.add(var.add_section(position, page))

        # Set pressed_style properties
        pressed_style = section_conf[CONF_PRESSED_STYLE]
        for prop_name in [CONF_COLOR, CONF_TEXT_COLOR, CONF_TEXT2_COLOR, CONF_ICON_COLOR, CONF_STYLE]:
            if prop_name in pressed_style:
                cg.add(var.set_section_pressed_style(section_index, prop_name, pressed_style[prop_name]))

        # Process layouts
        for layout_index, layout_conf in enumerate(section_conf[CONF_LAYOUTS]):
            widget_type = layout_conf[CONF_WIDGET]

            # Add layout
            cg.add(var.add_layout(section_index, widget_type))

            # Set condition if present
            if CONF_WHEN in layout_conf:
                when_lambda = await cg.process_lambda(
                    layout_conf[CONF_WHEN], [], return_type=cg.bool_
                )
                cg.add(var.set_layout_condition(section_index, layout_index, when_lambda))

            # Set widget properties (string or lambda)
            properties = [
                (CONF_COLOR, 'color'),
                (CONF_TEXT, 'text'),
                (CONF_TEXT_COLOR, 'text_color'),
                (CONF_TEXT2, 'text2'),
                (CONF_TEXT2_COLOR, 'text2_color'),
                (CONF_ICON_ID, 'icon_id'),
                (CONF_ICON_COLOR, 'icon_color'),
                (CONF_STYLE, 'style'),
                (CONF_GRAPH_COLOR, 'graph_color'),
                (CONF_VALUE, 'value'),
            ]

            for conf_key, prop_name in properties:
                value = layout_conf.get(conf_key)
                if value is not None:
                    if isinstance(value, cv.Lambda):
                        template_ = await cg.process_lambda(
                            value, [], return_type=cg.optional.template(cg.std_string)
                        )
                        cg.add(var.set_layout_property_template(
                            section_index, layout_index, prop_name, template_
                        ))
                    else:
                        cg.add(var.set_layout_property_static(
                            section_index, layout_index, prop_name, value
                        ))

            # Set graph params for gauge/line_graph
            if widget_type in ('gauge', 'line_graph'):
                cg.add(var.set_layout_graph_params(
                    section_index, layout_index,
                    layout_conf[CONF_MIN], layout_conf[CONF_MAX], layout_conf[CONF_SIZE]
                ))

            # Status items
            if CONF_ITEMS in layout_conf:
                for item in layout_conf[CONF_ITEMS]:
                    cg.add(var.add_layout_status_item(section_index, layout_index, item))

            # Chips
            if CONF_CHIPS in layout_conf:
                for chip in layout_conf[CONF_CHIPS]:
                    cg.add(var.add_layout_chip(
                        section_index, layout_index,
                        chip.get(CONF_ICON_ID, ''),
                        chip.get(CONF_LABEL, ''),
                        chip.get(CONF_BG_COLOR, ''),
                    ))

            # Auto-register notification service
            if widget_type == 'notification' and CONF_SERVICE_ID in layout_conf:
                service_id = layout_conf[CONF_SERVICE_ID]
                cg.add(var.add_notification_service(service_id, position, page))
                cg.add_define("USE_API_SERVICES")

        # Per-section on_press triggers
        for conf in section_conf.get(CONF_ON_PRESS, []):
            trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID])
            await automation.build_automation(trigger, [], conf)
            cg.add(var.register_section_press_trigger(section_index, trigger))

        # Per-section on_release triggers
        for conf in section_conf.get(CONF_ON_RELEASE, []):
            trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID])
            await automation.build_automation(trigger, [], conf)
            cg.add(var.register_section_release_trigger(section_index, trigger))

    # Component-level automations
    for conf in config.get(CONF_ON_CONNECTED, []):
        trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [], conf)

    for conf in config.get(CONF_ON_DISCONNECTED, []):
        trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [], conf)

    for conf in config.get(CONF_ON_SECTION_PRESS, []):
        trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [(cg.int_, 'position')], conf)

    for conf in config.get(CONF_ON_SECTION_RELEASE, []):
        trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [(cg.int_, 'position')], conf)

    for conf in config.get(CONF_ON_PAGE_CHANGE, []):
        trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [(cg.int_, 'page'), (cg.int_, 'page_count')], conf)

    for conf in config.get(CONF_ON_MODE_CHANGE, []):
        trigger = cg.new_Pvariable(conf[automation.CONF_TRIGGER_ID], var)
        await automation.build_automation(trigger, [(cg.std_string, 'mode')], conf)
