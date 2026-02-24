package transport

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"ulanzi-d200/pkg/state"
)

// Configuration
const (
	MAX_MSG_SIZE = 65536 // 64KB for batched commands
)

// Message Types
type Command struct {
	Type   string   `json:"type"`   // "command"
	Module string   `json:"module"` // "display", "sensor", "system"
	Action string   `json:"action"` // "set_brightness", "get_temp", etc.
	Params []string `json:"params"` // Parameters (plain strings, no encoding needed)
}

type Response struct {
	Status  string `json:"status"`            // "ok", "error"
	Code    int    `json:"code,omitempty"`     // 0=success, 1=error, 2=invalid, 3=timeout, 5=internal
	Message string `json:"message,omitempty"` // Human-readable (errors only)
	Data    string `json:"data,omitempty"`
}

type BatchMessage struct {
	Type     string    `json:"type"`
	Commands []Command `json:"commands"`
}

// Error Codes
const (
	CODE_OK       = 0
	CODE_ERROR    = 1
	CODE_INVALID  = 2
	CODE_TIMEOUT  = 3
	CODE_UNAVAIL  = 4
	CODE_INTERNAL = 5
)

// PageChangeHandler is called when the current page changes (notifies ESP32)
var PageChangeHandler func(pageID int, pageCount int, label string)

// ModeChangeHandler is called when a set_mode command is received via serial
var ModeChangeHandler func(mode string)

// DisplayDirtyHandler is called after state-mutating commands to trigger a redraw.
// buttonID >= 0 marks a single button dirty; buttonID < 0 marks all buttons dirty.
var DisplayDirtyHandler func(buttonID int)

// parseCommand parses NDJSON command protocol
// Format: {"type":"command","module":"...","action":"...","params":[...]}
func parseCommand(line string) (*Command, error) {
	var msg Command

	// Parse JSON
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Validate required fields
	if msg.Module == "" {
		return nil, fmt.Errorf("missing 'module' field")
	}
	if msg.Action == "" {
		return nil, fmt.Errorf("missing 'action' field")
	}

	// Set default type if not specified
	if msg.Type == "" {
		msg.Type = "command"
	}

	// Validate module
	validModules := map[string]bool{
		"display": true,
		"sensor":  true,
		"system":  true,
		"debug":   true,
		"button":  true,
		"page":    true,
	}
	if !validModules[msg.Module] {
		return nil, fmt.Errorf("invalid module: %s", msg.Module)
	}

	// Ensure params is not nil
	if msg.Params == nil {
		msg.Params = []string{}
	}

	return &msg, nil
}

// handleCommand executes commands locally
func handleCommand(msg *Command) Response {
	switch msg.Module {
	case "system":
		return handleSystemCommand(msg.Action, msg.Params)
	case "display":
		return handleDisplayCommand(msg.Action, msg.Params)
	case "sensor":
		return handleSensorCommand(msg.Action, msg.Params)
	case "debug":
		return handleDebugCommand(msg.Action, msg.Params)
	case "button":
		return handleButtonCommand(msg.Action, msg.Params)
	case "page":
		return handlePageCommand(msg.Action, msg.Params)
	default:
		return Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Unknown module: %s", msg.Module),
		}
	}
}

// handleSystemCommand handles system module commands
func handleSystemCommand(action string, params []string) Response {
	switch action {
	case "ping":
		return Response{
			Status: "ok",
			Data:   "pong",
		}
	case "version":
		return Response{
			Status: "ok",
			Data:   "ulanzi-control v1.0.0",
		}
	case "stats":
		return Response{
			Status: "ok",
			Data:   "uptime=0,commands=0",
		}
	case "set_mode":
		if len(params) < 1 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing mode parameter (run, calib, layout)",
			}
		}
		newMode := params[0]
		if newMode != "run" && newMode != "calib" && newMode != "layout" {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: fmt.Sprintf("Invalid mode: %s (must be run, calib, layout)", newMode),
			}
		}
		if ModeChangeHandler != nil {
			ModeChangeHandler(newMode)
		}
		return Response{Status: "ok"}
	case "set_time":
		if len(params) < 1 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing datetime parameter (e.g. '2026-02-14 12:30:45')",
			}
		}
		datetime := params[0]
		out, err := exec.Command("date", "-s", datetime).CombinedOutput()
		if err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_ERROR,
				Message: fmt.Sprintf("Failed to set time: %v (%s)", err, strings.TrimSpace(string(out))),
			}
		}
		return Response{Status: "ok"}
	default:
		return Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Unknown system action: %s", action),
		}
	}
}

// handleDisplayCommand handles display module commands
func handleDisplayCommand(action string, params []string) Response {
	switch action {
	case "set_brightness":
		if len(params) < 1 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing brightness parameter",
			}
		}
		pct, err := strconv.Atoi(params[0])
		if err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: fmt.Sprintf("Invalid brightness value: %s", params[0]),
			}
		}
		// Clamp to 0-100
		if pct < 0 {
			pct = 0
		} else if pct > 100 {
			pct = 100
		}
		// Scale 0-100 → 0-255
		sysVal := int(math.Round(float64(pct) * 255.0 / 100.0))
		if err := os.WriteFile("/sys/class/backlight/backlight/brightness", []byte(strconv.Itoa(sysVal)), 0644); err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_ERROR,
				Message: fmt.Sprintf("Failed to set brightness: %v", err),
			}
		}
		// Notify ESP32 of brightness change
		Send(Message{
			Type: "event",
			Payload: map[string]interface{}{
				"type":  "brightness",
				"value": pct,
			},
		})
		return Response{Status: "ok"}
	case "get_brightness":
		data, err := os.ReadFile("/sys/class/backlight/backlight/brightness")
		if err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_ERROR,
				Message: fmt.Sprintf("Failed to read brightness: %v", err),
			}
		}
		sysVal, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_ERROR,
				Message: fmt.Sprintf("Invalid brightness value from sysfs: %v", err),
			}
		}
		pct := int(math.Round(float64(sysVal) * 100.0 / 255.0))
		// Notify ESP32 of current brightness
		Send(Message{
			Type: "event",
			Payload: map[string]interface{}{
				"type":  "brightness",
				"value": pct,
			},
		})
		return Response{Status: "ok", Data: fmt.Sprintf("%d", pct)}
	case "show_text":
		if len(params) < 1 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing text parameter",
			}
		}
		return Response{Status: "ok"}
	default:
		return Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Unknown display action: %s", action),
		}
	}
}

// handleSensorCommand handles sensor module commands
func handleSensorCommand(action string, params []string) Response {
	switch action {
	case "get_temp":
		return Response{Status: "ok", Data: "25.5"}
	case "get_humidity":
		return Response{Status: "ok", Data: "45.0"}
	default:
		return Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Unknown sensor action: %s", action),
		}
	}
}

// handleDebugCommand handles debug module commands
func handleDebugCommand(action string, params []string) Response {
	switch action {
	case "echo":
		if len(params) < 1 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing echo parameter",
			}
		}
		return Response{Status: "ok", Data: params[0]}
	default:
		return Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Unknown debug action: %s", action),
		}
	}
}

// handleButtonCommand handles button module commands
func handleButtonCommand(action string, params []string) Response {
	switch action {
	case "reset_all":
		// Reset all button slots to defaults (called before ESP32 sends new configs)
		state.GlobalStore.ResetAllButtons()
		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(-1)
		}
		return Response{Status: "ok"}

	case "set_config":
		// Params: <button_id> <json_config> <persist> [page_id]
		if len(params) < 2 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, json_config)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		var config state.ButtonConfig
		if err := json.Unmarshal([]byte(params[1]), &config); err != nil {
			log.Printf("set_config: JSON unmarshal failed for button %d: %v", buttonID, err)
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: fmt.Sprintf("Invalid JSON config: %v", err),
			}
		}

		if config.CurrentState == "" {
			config.CurrentState = "default"
		}

		persist := true
		if len(params) >= 3 && params[2] == "false" {
			persist = false
		}

		pageID := state.GlobalStore.CurrentPage
		if len(params) >= 4 {
			fmt.Sscanf(params[3], "%d", &pageID)
		}

		if pageID == -1 {
			for _, pid := range state.GlobalStore.GetPagesList() {
				state.GlobalStore.UpdateButtonWithPersist(pid, buttonID, config, persist)
			}
		} else {
			state.GlobalStore.UpdateButtonWithPersist(pageID, buttonID, config, persist)
		}

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok"}

	case "get_config":
		// Params: <button_id> [page_id]
		if len(params) < 1 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing button_id parameter",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		// Optional page_id parameter
		var config state.ButtonConfig
		if len(params) >= 2 {
			var pageID int
			fmt.Sscanf(params[1], "%d", &pageID)
			config = state.GlobalStore.GetButton(pageID, buttonID)
		} else {
			config = state.GlobalStore.GetCurrentButton(buttonID)
		}

		jsonData, err := json.Marshal(config)
		if err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_INTERNAL,
				Message: fmt.Sprintf("Failed to serialize config: %v", err),
			}
		}

		return Response{Status: "ok", Data: string(jsonData)}

	case "set_state":
		// Params: <button_id> <state>
		if len(params) < 2 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, state)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		stateName := params[1]
		if stateName != "default" && stateName != "active" && stateName != "pressed" {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid state (must be: default, active, pressed)",
			}
		}

		state.GlobalStore.SetCurrentButtonState(buttonID, stateName)

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok"}

	case "push_value":
		// Params: <button_id> <float_value> [page_id]
		if len(params) < 2 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, value)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		value, err := strconv.ParseFloat(params[1], 64)
		if err != nil {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: fmt.Sprintf("Invalid float value: %s", params[1]),
			}
		}

		pageID := state.GlobalStore.CurrentPage
		if len(params) >= 3 {
			fmt.Sscanf(params[2], "%d", &pageID)
		}

		state.GlobalStore.PushGraphValue(pageID, buttonID, value)

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok"}

	case "update_display":
		// Params: <button_id> <json_properties> [page_id]
		if len(params) < 2 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, json_properties)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		// Parse optional page_id (default: current page)
		pageID := state.GlobalStore.CurrentPage
		if len(params) >= 3 {
			fmt.Sscanf(params[2], "%d", &pageID)
		}

		// Parse JSON properties
		var properties map[string]string
		if err := json.Unmarshal([]byte(params[1]), &properties); err != nil {
			log.Printf("update_display: JSON unmarshal failed for button %d: %v", buttonID, err)
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: fmt.Sprintf("Invalid JSON properties: %v", err),
			}
		}

		// Helper to update a single button on a specific page
		// Apply display updates to ALL states (not just current) since the
		// ESP32 builds all states from the same template values.
		updateButtonOnPage := func(pid int) {
			btn := state.GlobalStore.GetButton(pid, buttonID)
			if btn.States == nil {
				btn.States = make(map[string]state.ButtonState)
			}
			if len(btn.States) == 0 {
				btn.States["default"] = state.ButtonState{}
			}
			for stateName, stateData := range btn.States {
				if val, ok := properties["text"]; ok { stateData.Text = val }
				if val, ok := properties["text2"]; ok { stateData.Text2 = val }
				if val, ok := properties["icon_id"]; ok { stateData.IconID = val }
				if val, ok := properties["color"]; ok { stateData.Color = val }
				if val, ok := properties["text_color"]; ok { stateData.TextColor = val }
				if val, ok := properties["text2_color"]; ok { stateData.Text2Color = val }
				if val, ok := properties["icon_color"]; ok { stateData.IconColor = val }
				if val, ok := properties["style"]; ok { stateData.Style = val }
				if val, ok := properties["graph_color"]; ok { stateData.GraphColor = val }
				if val, ok := properties["value"]; ok { stateData.Value = val }
				btn.States[stateName] = stateData
			}
			state.GlobalStore.UpdateButtonWithPersist(pid, buttonID, btn, false)
		}

		if pageID == -1 {
			// All-pages button: update on every page
			for _, pid := range state.GlobalStore.GetPagesList() {
				updateButtonOnPage(pid)
			}
		} else {
			updateButtonOnPage(pageID)
		}

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok"}

	case "add_notification":
		// Params: <button_id> <page_id> <message>
		if len(params) < 3 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, page_id, message)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		var pageID int
		fmt.Sscanf(params[1], "%d", &pageID)

		message := params[2]
		if message == "" {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Message cannot be empty",
			}
		}

		// Generate notification ID
		notificationID := fmt.Sprintf("notif_%d", time.Now().UnixNano())

		notification := state.Notification{
			ID:        notificationID,
			Message:   message,
			Timestamp: time.Now().Unix(),
		}

		state.GlobalStore.AddNotification(pageID, buttonID, notification)

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok", Data: notificationID}

	case "clear_notification":
		// Params: <button_id> <page_id> <notification_id>
		if len(params) < 3 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, page_id, notification_id)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		var pageID int
		fmt.Sscanf(params[1], "%d", &pageID)

		notificationID := params[2]
		state.GlobalStore.ClearNotification(pageID, buttonID, notificationID)

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok"}

	case "clear_all_notifications":
		// Params: <button_id> <page_id>
		if len(params) < 2 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Missing parameters (need: button_id, page_id)",
			}
		}

		var buttonID int
		fmt.Sscanf(params[0], "%d", &buttonID)
		if buttonID < 0 || buttonID > 14 {
			return Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: "Invalid button ID (must be 0-14)",
			}
		}

		var pageID int
		fmt.Sscanf(params[1], "%d", &pageID)

		state.GlobalStore.ClearAllNotifications(pageID, buttonID)

		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(buttonID)
		}
		return Response{Status: "ok"}

	default:
		return Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Unknown button action: %s", action),
		}
	}
}

// resolvePageID resolves a page identifier (numeric ID or label name) to a page ID.
func resolvePageID(param string) (int, bool) {
	// Try numeric first
	var pageID int
	if _, err := fmt.Sscanf(param, "%d", &pageID); err == nil {
		if _, exists := state.GlobalStore.Pages[pageID]; exists {
			return pageID, true
		}
	}
	// Try label lookup
	if id, ok := state.GlobalStore.GetPageByLabel(param); ok {
		return id, true
	}
	return 0, false
}

// notifyPageChange fires the page change handler with label info.
func notifyPageChange(pageID int, pageCount int) {
	if PageChangeHandler != nil {
		label := state.GlobalStore.GetPageLabel(pageID)
		PageChangeHandler(pageID, pageCount, label)
	}
}

// handlePageCommand handles page module commands
func handlePageCommand(action string, params []string) Response {
	switch action {
	case "get":
		pageID := state.GlobalStore.CurrentPage
		label := state.GlobalStore.GetPageLabel(pageID)
		pages := state.GlobalStore.GetPagesList()
		sort.Ints(pages)
		dataMap := map[string]interface{}{
			"page":       pageID,
			"label":      label,
			"page_count": len(pages),
		}
		jsonData, _ := json.Marshal(dataMap)
		return Response{Status: "ok", Data: string(jsonData)}

	case "set":
		if len(params) < 1 {
			return Response{Status: "error", Code: CODE_INVALID, Message: "Missing page_id or name parameter"}
		}
		pageID, ok := resolvePageID(params[0])
		if !ok {
			return Response{Status: "error", Code: CODE_INVALID, Message: fmt.Sprintf("Page not found: %s", params[0])}
		}
		state.GlobalStore.SetPage(pageID)
		pages := state.GlobalStore.GetPagesList()
		notifyPageChange(pageID, len(pages))
		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(-1)
		}
		return Response{Status: "ok"}

	case "next":
		pages := state.GlobalStore.GetPagesList()
		sort.Ints(pages)
		curr := state.GlobalStore.CurrentPage
		next := pages[0]
		for i, p := range pages {
			if p == curr && i+1 < len(pages) {
				next = pages[i+1]
				break
			}
		}
		if curr == pages[len(pages)-1] {
			next = pages[0]
		}
		state.GlobalStore.SetPage(next)
		notifyPageChange(next, len(pages))
		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(-1)
		}
		return Response{Status: "ok"}

	case "prev":
		pages := state.GlobalStore.GetPagesList()
		sort.Ints(pages)
		curr := state.GlobalStore.CurrentPage
		prev := pages[len(pages)-1]
		for i, p := range pages {
			if p == curr && i > 0 {
				prev = pages[i-1]
				break
			}
		}
		state.GlobalStore.SetPage(prev)
		notifyPageChange(prev, len(pages))
		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(-1)
		}
		return Response{Status: "ok"}

	case "add":
		label := ""
		if len(params) >= 1 {
			label = params[0]
		}
		newID := state.GlobalStore.AddPageWithLabel(label)
		if DisplayDirtyHandler != nil {
			DisplayDirtyHandler(-1)
		}
		return Response{Status: "ok", Data: fmt.Sprintf("%d", newID)}

	case "rename":
		if len(params) < 2 {
			return Response{Status: "error", Code: CODE_INVALID, Message: "Missing parameters (need: page_id, new_label)"}
		}
		var pageID int
		fmt.Sscanf(params[0], "%d", &pageID)
		newLabel := params[1]
		if err := state.GlobalStore.SetPageLabel(pageID, newLabel); err != nil {
			return Response{Status: "error", Code: CODE_ERROR, Message: err.Error()}
		}
		return Response{Status: "ok"}

	case "list":
		pagesInfo := state.GlobalStore.GetPagesInfo()
		sort.Slice(pagesInfo, func(i, j int) bool { return pagesInfo[i].ID < pagesInfo[j].ID })
		jsonData, _ := json.Marshal(pagesInfo)
		return Response{Status: "ok", Data: string(jsonData)}

	default:
		return Response{Status: "error", Code: CODE_INVALID, Message: fmt.Sprintf("Unknown page action: %s", action)}
	}
}
