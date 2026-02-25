package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"ulanzi-d200/pkg/state"
	"ulanzi-d200/pkg/transport"
	"ulanzi-d200/pkg/ui"
)

const (
	FB_DEVICE   = "/dev/fb0"
	INPUT_DEV   = "/dev/input/event0"
	WIDTH       = 720
	HEIGHT      = 1280
	BPP         = 4
	KEYMAP_FILE = "config/keymap.json"
	LAYOUT_FILE = "config/layout.json"
	LOG_WIDTH   = 1280
	LOG_HEIGHT  = 720
)

var keyMap = map[uint16]int{}

type KeyMapJSON struct {
	Keys []int `json:"keys"`
}

type LayoutConfig struct {
	OffsetX int `json:"offset_x"`
	OffsetY int `json:"offset_y"`
	BtnW    int `json:"btn_w"`
	BtnH    int `json:"btn_h"`
	GapX    int `json:"gap_x"`
	GapY    int `json:"gap_y"`
}

var layout = LayoutConfig{
	OffsetX: 15,
	OffsetY: 3,
	BtnW:    200,
	BtnH:    200,
	GapX:    60,
	GapY:    60,
}

var mode string
var editMode int
var modeMu sync.RWMutex
var calibIndex int
var calibKeys [14]int

// Per-button dirty flags — only redraw buttons that actually changed
var dirtyButtons [14]int32
var dirtyAll int32

// Persistent image buffer — allocated once, reused every frame
var displayImg *image.RGBA

// markButtonDirty marks a single button for redraw on the next tick.
func markButtonDirty(id int) {
	if id >= 0 && id < 14 {
		atomic.StoreInt32(&dirtyButtons[id], 1)
	}
}

// markAllDirty marks all buttons for a full redraw on the next tick.
func markAllDirty() {
	atomic.StoreInt32(&dirtyAll, 1)
}

// markDirty is an alias for markAllDirty (backward compat).
func markDirty() {
	markAllDirty()
}

// markAnimatedButtonsDirty marks specific buttons that need continuous redraws
// (animated styles like "fast"/"slow", clock widget, scrolling notifications).
func markAnimatedButtonsDirty() {
	for i := 0; i < 14; i++ {
		btn := state.GlobalStore.GetCurrentButton(i)
		curr := btn.CurrentState
		if s, ok := btn.States[curr]; ok {
			if s.Style == "fast" || s.Style == "slow" {
				markButtonDirty(i)
			}
		}
		if btn.WideLayout == "status" || btn.WideLayout == "notification" {
			markButtonDirty(i)
		}
	}
}


// sendPageChangeEvent notifies the ESP32 about a page change
func sendPageChangeEvent(pageID int, pageCount int, label string) {
	transport.Send(transport.Message{
		Type: "event",
		Payload: map[string]interface{}{
			"type":       "page_change",
			"page":       pageID,
			"page_count": pageCount,
			"label":      label,
		},
	})
	fmt.Printf("Page change event: page=%d, count=%d, label=%s\n", pageID, pageCount, label)
}

func getMode() string {
	modeMu.RLock()
	defer modeMu.RUnlock()
	return mode
}

func setMode(newMode string) {
	modeMu.Lock()
	defer modeMu.Unlock()

	// Exit actions
	if mode == "layout" {
		saveLayout()
		fmt.Println("Layout auto-saved")
	}

	// Enter actions
	if newMode == "calib" {
		calibIndex = 0
		calibKeys = [14]int{}
	}

	mode = newMode
	fmt.Printf("Mode changed to: %s\n", newMode)
	markDirty()

	// Notify ESP32
	transport.Send(transport.Message{
		Type: "event",
		Payload: map[string]interface{}{
			"type": "mode_change",
			"mode": newMode,
		},
	})
}

// handlePageNavigation handles page-next, page-prev, and page-jump button types.
// Returns true if the button was a page navigation button and was handled.
func handlePageNavigation(btn state.ButtonConfig) bool {
	pages := state.GlobalStore.GetPagesList()
	sort.Ints(pages)
	curr := state.GlobalStore.CurrentPage

	switch btn.Type {
	case "page-next":
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
		sendPageChangeEvent(next, len(pages), state.GlobalStore.GetPageLabel(next))
		fmt.Println("Page Next ->", next)
		markDirty()
		return true

	case "page-prev":
		prev := pages[len(pages)-1]
		for i, p := range pages {
			if p == curr && i > 0 {
				prev = pages[i-1]
				break
			}
		}
		state.GlobalStore.SetPage(prev)
		sendPageChangeEvent(prev, len(pages), state.GlobalStore.GetPageLabel(prev))
		fmt.Println("Page Prev ->", prev)
		markDirty()
		return true

	case "page-jump":
		target := btn.TargetPage
		if _, ok := state.GlobalStore.Pages[target]; ok {
			state.GlobalStore.SetPage(target)
			sendPageChangeEvent(target, len(pages), state.GlobalStore.GetPageLabel(target))
			fmt.Println("Page Jump ->", target)
			markDirty()
		} else {
			fmt.Printf("Page Jump: invalid target page %d\n", target)
		}
		return true
	}

	return false
}

func main() {
	mode = "run"

	if err := os.MkdirAll("config", 0755); err != nil {
		fmt.Println("Failed to create config dir:", err)
	}

	state.Init()

	if err := ui.Init(); err != nil {
		fmt.Println("UI Init failed:", err)
	}

	if err := transport.Init(); err != nil {
		fmt.Println("Transport Init failed:", err)
	}

	// Start serial reader for bidirectional NDJSON communication with ESP32
	transport.StartSerialReader()

	// Register handlers for serial commands
	transport.PageChangeHandler = sendPageChangeEvent
	transport.ModeChangeHandler = setMode
	transport.DisplayDirtyHandler = func(id int) {
		if id < 0 {
			markAllDirty()
		} else {
			markButtonDirty(id)
		}
	}

	// Allocate persistent image buffer (reused every frame)
	displayImg = image.NewRGBA(image.Rect(0, 0, LOG_WIDTH, LOG_HEIGHT))

	fFb, err := os.OpenFile(FB_DEVICE, os.O_RDWR, 0)
	if err != nil { panic(err) }
	size := WIDTH * HEIGHT * BPP
	fbData, err := syscall.Mmap(int(fFb.Fd()), 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil { panic(err) }

	// Unblank the framebuffer (FBIOBLANK = 0x4611, FB_BLANK_UNBLANK = 0)
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fFb.Fd(), 0x4611, 0)
	if errno != 0 {
		fmt.Println("FBIOBLANK failed:", errno)
	} else {
		fmt.Println("Framebuffer unblanked")
	}

	fIn, err := os.Open(INPUT_DEV)
	if err != nil { panic(err) }

	loadLayout()
	loadKeyMap()

	runControl(fbData, fIn)
}

func runControl(fb []byte, fIn *os.File) {
	fmt.Println("Ulanzi Control Running...")

	// Notify ESP32 that the daemon is ready
	pages := state.GlobalStore.GetPagesList()
	transport.Send(transport.Message{
		Type: "event",
		Payload: map[string]interface{}{
			"type":       "ready",
			"page":       state.GlobalStore.CurrentPage,
			"page_count": len(pages),
		},
	})
	fmt.Println("Sent ready event to ESP32")

	// Send current brightness so ESP32 syncs on boot
	if brightnessData, err := os.ReadFile("/sys/class/backlight/backlight/brightness"); err == nil {
		if sysVal, err := strconv.Atoi(strings.TrimSpace(string(brightnessData))); err == nil {
			pct := int(math.Round(float64(sysVal) * 100.0 / 255.0))
			transport.Send(transport.Message{
				Type: "event",
				Payload: map[string]interface{}{
					"type":  "brightness",
					"value": pct,
				},
			})
			fmt.Printf("Sent brightness event: %d%%\n", pct)
		}
	}

	markDirty()

	ticker := time.NewTicker(50 * time.Millisecond)
	go func() {
		for range ticker.C {
			// Auto-mark animated buttons dirty
			markAnimatedButtonsDirty()
			// Full redraw if dirtyAll is set
			if atomic.CompareAndSwapInt32(&dirtyAll, 1, 0) {
				// Clear per-button flags (full redraw covers everything)
				for i := range dirtyButtons {
					atomic.StoreInt32(&dirtyButtons[i], 0)
				}
				updateDisplay(fb)
			} else {
				// Partial redraw: only redraw dirty buttons
				for i := 0; i < 14; i++ {
					if atomic.CompareAndSwapInt32(&dirtyButtons[i], 1, 0) {
						redrawButton(fb, i)
					}
				}
			}
		}
	}()

	// Autoscroll ticker for notification widgets (100ms for smooth scrolling)
	scrollTicker := time.NewTicker(100 * time.Millisecond)
	go func() {
		for range scrollTicker.C {
			updateNotificationScrolls()
		}
	}()

	buf := make([]byte, 16)
	for {
		_, err := fIn.Read(buf)
		if err != nil { break }

		typ := binary.LittleEndian.Uint16(buf[8:10])
		code := binary.LittleEndian.Uint16(buf[10:12])
		val := int32(binary.LittleEndian.Uint32(buf[12:16]))

		if typ == 1 {
			m := getMode()

			if m == "calib" {
				handleCalibInput(code, val)
				continue
			}

			idx, ok := keyMap[code]
			if !ok {
				continue
			}

			if m == "layout" {
				if val == 1 {
					handleLayoutInput(idx)
				}
				continue
			}

			// Run mode
			btn := state.GlobalStore.GetCurrentButton(idx)

			if val == 1 { // Press
				// Handle page navigation buttons
				if handlePageNavigation(btn) {
					continue
				}

				// Handle notification widget button press (dismiss the currently shown message)
				if btn.WideLayout == "notification" && len(btn.Notifications) > 0 {
					latest := btn.Notifications[len(btn.Notifications)-1]
					state.GlobalStore.ClearNotification(-1, idx, latest.ID)
					markButtonDirty(idx)
					continue
				}

				// Handle button press based on type
				if btn.Type == "toggle" {
					// Toggle buttons: Send active/inactive state
					newState := "active"
					eventState := "active"
					if btn.CurrentState == "active" {
						newState = "default"
						eventState = "inactive"
					}
					state.GlobalStore.SetCurrentButtonState(idx, newState)

					// Send state change event
					transport.Send(transport.Message{
						Type: "event",
						Payload: map[string]interface{}{
							"btn":   idx,
							"state": eventState,
							"page":  state.GlobalStore.CurrentPage,
						},
					})
				} else {
					// Momentary buttons: Send pressed event
					transport.Send(transport.Message{
						Type: "event",
						Payload: map[string]interface{}{
							"btn":   idx,
							"state": "pressed",
							"page":  state.GlobalStore.CurrentPage,
						},
					})
					state.GlobalStore.SetCurrentButtonState(idx, "pressed")
				}
			} else if val == 0 { // Release
				// Only handle release for non-page and non-toggle buttons
				if btn.Type != "toggle" && btn.Type != "page-next" && btn.Type != "page-prev" && btn.Type != "page-jump" {
					// Send released event for momentary buttons
					transport.Send(transport.Message{
						Type: "event",
						Payload: map[string]interface{}{
							"btn":   idx,
							"state": "released",
							"page":  state.GlobalStore.CurrentPage,
						},
					})
					state.GlobalStore.SetCurrentButtonState(idx, "default")
				}
			}
			markButtonDirty(idx)
		}
	}
}

func handleCalibInput(code uint16, val int32) {
	if val == 1 { // Press
		calibKeys[calibIndex] = int(code)
	} else if val == 0 { // Release
		calibIndex++
		if calibIndex >= 14 {
			data, _ := json.Marshal(KeyMapJSON{Keys: calibKeys[:]})
			if err := os.WriteFile(KEYMAP_FILE, data, 0644); err != nil {
				fmt.Println("Failed to save keymap:", err)
			}
			loadKeyMap()
			fmt.Println("Calibration complete, keymap saved")
			setMode("layout")
			return
		}
	}
	markAllDirty()
}

func handleLayoutInput(idx int) {
	switch idx {
	case 0: editMode = 0
	case 1: editMode = 1
	case 2: editMode = 2
	case 3: editMode = 3
	case 5: editMode = 4
	case 6: editMode = 5
	case 10: changeValue(-1)
	case 11: changeValue(1)
	case 13: setMode("run"); return
	}
	fmt.Printf("Layout mode: editMode=%d, layout=%+v\n", editMode, layout)
	markAllDirty()
}


func changeValue(delta int) {
	switch editMode {
	case 0: layout.OffsetX += delta
	case 1: layout.OffsetY += delta
	case 2: layout.GapX += delta
	case 3: layout.GapY += delta
	case 4: layout.BtnW += delta
	case 5: layout.BtnH += delta
	}
	if layout.BtnW < 10 { layout.BtnW = 10 }
	if layout.BtnH < 10 { layout.BtnH = 10 }
}

// defaultKeyMapKeys is the factory calibration for the D200 touch controller.
// Overridden by config/keymap.json after user calibration.
var defaultKeyMapKeys = []int{29, 15, 14, 13, 12, 11, 10, 9, 8, 7, 34, 33, 31, 30}

func loadKeyMap() {
	km := KeyMapJSON{Keys: defaultKeyMapKeys}
	if file, err := os.ReadFile(KEYMAP_FILE); err == nil {
		if err := json.Unmarshal(file, &km); err != nil {
			fmt.Println("Failed to parse keymap:", err)
		}
	}
	for i, code := range km.Keys {
		keyMap[uint16(code)] = i
	}
}

func loadLayout() {
	file, err := os.ReadFile(LAYOUT_FILE)
	if err == nil {
		if err := json.Unmarshal(file, &layout); err != nil {
			fmt.Println("Failed to parse layout:", err)
		}
	}
	if layout.BtnW == 0 { layout.BtnW = 240 }
	if layout.BtnH == 0 { layout.BtnH = 240 }
}

func saveLayout() {
	data, _ := json.Marshal(layout)
	if err := os.WriteFile(LAYOUT_FILE, data, 0644); err != nil {
		fmt.Println("Failed to save layout:", err)
	}
}

// buttonRect computes the rectangle for button i based on the current layout.
func buttonRect(i int) image.Rectangle {
	c := i % 5
	r := i / 5
	w := layout.BtnW
	h := layout.BtnH
	gx := layout.GapX
	gy := layout.GapY
	x0 := layout.OffsetX + c*(w+gx)
	y0 := layout.OffsetY + r*(h+gy)
	if i == 13 {
		return image.Rect(x0, y0, x0+2*w+gx, y0+h)
	}
	return image.Rect(x0, y0, x0+w, y0+h)
}

// drawButtonContent renders a single button's background and content onto img.
func drawButtonContent(img *image.RGBA, i int, rect image.Rectangle, currentMode string) {
	col := color.RGBA{100, 100, 100, 255}

	if currentMode == "calib" {
		if i == calibIndex {
			col = color.RGBA{200, 0, 0, 255}
		} else if i < calibIndex {
			col = color.RGBA{0, 200, 0, 255}
		}
	} else if currentMode == "layout" {
		if i==0 { col = color.RGBA{100, 0, 0, 255}; if editMode==0 { col = color.RGBA{255, 0, 0, 255} } }
		if i==1 { col = color.RGBA{0, 100, 0, 255}; if editMode==1 { col = color.RGBA{0, 0, 255, 255} } }
		if i==2 { col = color.RGBA{0, 0, 100, 255}; if editMode==2 { col = color.RGBA{0, 0, 255, 255} } }
		if i==3 { col = color.RGBA{100, 100, 0, 255}; if editMode==3 { col = color.RGBA{255, 255, 0, 255} } }

		if i==5 { col = color.RGBA{0, 100, 100, 255}; if editMode==4 { col = color.RGBA{0, 255, 255, 255} } }
		if i==6 { col = color.RGBA{100, 0, 100, 255}; if editMode==5 { col = color.RGBA{255, 0, 255, 255} } }

		if i==10 { col = color.RGBA{150, 50, 50, 255} }
		if i==11 { col = color.RGBA{50, 150, 50, 255} }
		if i==13 { col = color.RGBA{200, 200, 200, 255} }
	} else {
		btn := state.GlobalStore.GetCurrentButton(i)
		curr := btn.CurrentState
		if s, ok := btn.States[curr]; ok {
			col = parseHexColor(s.Color)
			col = applyStyleModulation(col, s.Style)
		}
	}

	draw.Draw(img, rect, &image.Uniform{col}, image.Point{}, draw.Src)

	cx := rect.Min.X + (rect.Dx() / 2)
	cy := rect.Min.Y + (rect.Dy() / 2)

	if currentMode == "layout" {
		switch i {
		case 0:
			ui.DrawIcon(img, cx, cy-50, 36, "arrow-left-right", color.White)
			ui.DrawText(img, cx, cy, 22, "Offset X", color.White)
			ui.DrawText(img, cx, cy+40, 28, strconv.Itoa(layout.OffsetX), color.White)
		case 1:
			ui.DrawIcon(img, cx, cy-50, 36, "arrow-up-down", color.White)
			ui.DrawText(img, cx, cy, 22, "Offset Y", color.White)
			ui.DrawText(img, cx, cy+40, 28, strconv.Itoa(layout.OffsetY), color.White)
		case 2:
			ui.DrawIcon(img, cx, cy-50, 36, "arrow-expand-horizontal", color.White)
			ui.DrawText(img, cx, cy, 22, "Gap X", color.White)
			ui.DrawText(img, cx, cy+40, 28, strconv.Itoa(layout.GapX), color.White)
		case 3:
			ui.DrawIcon(img, cx, cy-50, 36, "arrow-expand-vertical", color.White)
			ui.DrawText(img, cx, cy, 22, "Gap Y", color.White)
			ui.DrawText(img, cx, cy+40, 28, strconv.Itoa(layout.GapY), color.White)
		case 5:
			ui.DrawIcon(img, cx, cy-50, 36, "arrow-expand-horizontal", color.White)
			ui.DrawText(img, cx, cy, 22, "Width", color.White)
			ui.DrawText(img, cx, cy+40, 28, strconv.Itoa(layout.BtnW), color.White)
		case 6:
			ui.DrawIcon(img, cx, cy-50, 36, "arrow-expand-vertical", color.White)
			ui.DrawText(img, cx, cy, 22, "Height", color.White)
			ui.DrawText(img, cx, cy+40, 28, strconv.Itoa(layout.BtnH), color.White)
		case 10:
			ui.DrawIcon(img, cx, cy, 64, "minus-thick", color.White)
		case 11:
			ui.DrawIcon(img, cx, cy, 64, "plus-thick", color.White)
		case 13:
			ui.DrawIcon(img, cx, cy-20, 48, "content-save", color.Black)
			ui.DrawText(img, cx, cy+30, 22, "Save & Exit", color.Black)
		}
	} else if currentMode == "calib" {
		ui.DrawText(img, cx, cy, 48, strconv.Itoa(i), color.White)
	} else {
		btn := state.GlobalStore.GetCurrentButton(i)
		curr := btn.CurrentState
		if s, ok := btn.States[curr]; ok {
			iconCol := parseHexColor(s.IconColor)
			if s.IconColor == "" { iconCol = color.RGBA{255,255,255,255} }

			textCol := parseHexColor(s.TextColor)
			if s.TextColor == "" { textCol = color.RGBA{255,255,255,255} }

			text2Col := parseHexColor(s.Text2Color)
			if s.Text2Color == "" { text2Col = color.RGBA{200,200,200,255} }

			switch btn.WideLayout {
			case "side-by-side":
				drawSideBySide(img, rect, s, iconCol, textCol, text2Col)
			case "status":
				drawStatusBar(img, rect, s, btn.StatusItems, textCol)
			case "chips":
				drawChips(img, rect, s)
			case "line-graph":
				drawLineGraph(img, rect, btn, s, textCol, text2Col)
			case "gauge":
				drawGauge(img, rect, btn, s, iconCol, textCol, text2Col)
			case "notification":
				drawNotification(img, rect, btn, s, textCol, text2Col)
			default:
				hasIcon := s.IconID != ""
				hasText1 := s.Text != ""
				hasText2 := s.Text2 != ""

				if hasIcon {
					if hasText1 && hasText2 {
						ui.DrawIcon(img, cx, cy-30, 48, s.IconID, iconCol)
						ui.DrawText(img, cx, cy+18, 28, s.Text, textCol)
						ui.DrawText(img, cx, rect.Min.Y+156, 28, s.Text2, text2Col)
					} else if hasText1 {
						ui.DrawIcon(img, cx, cy-30, 48, s.IconID, iconCol)
						ui.DrawText(img, cx, cy+18, 28, s.Text, textCol)
					} else {
						ui.DrawIcon(img, cx, cy, 80, s.IconID, iconCol)
					}
				} else {
					if hasText1 && hasText2 {
						ui.DrawText(img, cx, cy, 28, s.Text, textCol)
						ui.DrawText(img, cx, rect.Min.Y+156, 28, s.Text2, text2Col)
					} else if hasText1 {
						ui.DrawText(img, cx, cy, 32, s.Text, textCol)
					}
				}
			}

			if btn.Type == "page-next" {
			    ui.DrawIcon(img, rect.Max.X-20, rect.Max.Y-20, 24, "arrow-right-thick", color.RGBA{100,100,100,200})
			}
			if btn.Type == "page-prev" {
			    ui.DrawIcon(img, rect.Min.X+20, rect.Max.Y-20, 24, "arrow-left-thick", color.RGBA{100,100,100,200})
			}
			if btn.Type == "page-jump" {
			    ui.DrawIcon(img, rect.Max.X-20, rect.Min.Y+20, 24, "page-layout-body", color.RGBA{100,100,100,200})
			}
		}
	}
}

// rotateRect rotates only the given rect region from displayImg to framebuffer.
func rotateRect(fb []byte, img *image.RGBA, rect image.Rectangle) {
	src := img.Pix
	minX := rect.Min.X
	maxX := rect.Max.X
	minY := rect.Min.Y
	maxY := rect.Max.Y
	if minX < 0 { minX = 0 }
	if minY < 0 { minY = 0 }
	if maxX > LOG_WIDTH { maxX = LOG_WIDTH }
	if maxY > LOG_HEIGHT { maxY = LOG_HEIGHT }

	for ly := minY; ly < maxY; ly++ {
		srcRow := ly * LOG_WIDTH * 4
		for lx := minX; lx < maxX; lx++ {
			si := srcRow + lx*4
			di := ((LOG_WIDTH - 1 - lx) * WIDTH + ly) * 4
			fb[di+0] = src[si+2]
			fb[di+1] = src[si+1]
			fb[di+2] = src[si+0]
			fb[di+3] = src[si+3]
		}
	}
}

// redrawButton performs a partial redraw for a single button.
func redrawButton(fb []byte, i int) {
	rect := buttonRect(i)
	drawButtonContent(displayImg, i, rect, getMode())
	rotateRect(fb, displayImg, rect)
}

func updateDisplay(fb []byte) {
	currentMode := getMode()
	img := displayImg
	// Fast fill: set first row to background color, then copy-double to fill the rest
	bgPix := img.Pix
	stride := img.Stride
	for i := 0; i < stride; i += 4 {
		bgPix[i+0] = 20
		bgPix[i+1] = 20
		bgPix[i+2] = 20
		bgPix[i+3] = 255
	}
	for filled := stride; filled < len(bgPix); filled *= 2 {
		copy(bgPix[filled:], bgPix[:filled])
	}

	for i := 0; i < 14; i++ {
		rect := buttonRect(i)
		drawButtonContent(img, i, rect, currentMode)
	}

	src := img.Pix
	for ly := 0; ly < LOG_HEIGHT; ly++ {
		srcRow := ly * LOG_WIDTH * 4
		for lx := 0; lx < LOG_WIDTH; lx++ {
			si := srcRow + lx*4
			di := ((LOG_WIDTH - 1 - lx) * WIDTH + ly) * 4
			fb[di+0] = src[si+2]
			fb[di+1] = src[si+1]
			fb[di+2] = src[si+0]
			fb[di+3] = src[si+3]
		}
	}
}

// drawSideBySide renders icon on the left half, text+text2 stacked on the right half.
func drawSideBySide(img *image.RGBA, rect image.Rectangle, s state.ButtonState, iconCol, textCol, text2Col color.RGBA) {
	midX := rect.Min.X + rect.Dx()/2
	cy := rect.Min.Y + rect.Dy()/2

	// Left half: icon centered
	leftCX := rect.Min.X + rect.Dx()/4
	if s.IconID != "" {
		ui.DrawIcon(img, leftCX, cy, 72, s.IconID, iconCol)
	}

	// Right half: text stacked
	rightCX := midX + rect.Dx()/4
	if s.Text != "" && s.Text2 != "" {
		ui.DrawText(img, rightCX, cy-16, 28, s.Text, textCol)
		ui.DrawText(img, rightCX, cy+20, 20, s.Text2, text2Col)
	} else if s.Text != "" {
		ui.DrawText(img, rightCX, cy, 28, s.Text, textCol)
	}
}

// drawStatusBar renders status items (clock, page, etc.) in equal horizontal slots.
func drawStatusBar(img *image.RGBA, rect image.Rectangle, s state.ButtonState, items []string, textCol color.RGBA) {
	if len(items) == 0 {
		items = []string{"clock", "page"}
	}

	slotW := rect.Dx() / len(items)
	cy := rect.Min.Y + rect.Dy()/2

	for idx, item := range items {
		slotCX := rect.Min.X + slotW*idx + slotW/2

		switch item {
		case "clock":
			ui.DrawIcon(img, slotCX-40, cy, 36, "clock-outline", textCol)
			ui.DrawText(img, slotCX+20, cy, 28, time.Now().Format("15:04"), textCol)
		case "page":
			pages := state.GlobalStore.GetPagesList()
			current := state.GlobalStore.CurrentPage
			pageLabel := state.GlobalStore.GetPageLabel(current)
			if pageLabel == "" {
				pageLabel = fmt.Sprintf("%d/%d", current+1, len(pages))
			}
			ui.DrawIcon(img, slotCX-35, cy, 36, "book-open-page-variant-outline", textCol)
			ui.DrawText(img, slotCX+20, cy, 28, pageLabel, textCol)
		default:
			// Unknown item: render s.Text or the item name
			txt := s.Text
			if txt == "" {
				txt = item
			}
			ui.DrawText(img, slotCX, cy, 24, txt, textCol)
		}
	}
}

// drawChips renders up to 3 colored chip badges with icon + label.
func drawChips(img *image.RGBA, rect image.Rectangle, s state.ButtonState) {
	chips := s.Chips
	if len(chips) == 0 {
		return
	}
	if len(chips) > 3 {
		chips = chips[:3]
	}

	gap := 8
	totalGap := gap * (len(chips) - 1)
	chipW := (rect.Dx() - 16 - totalGap) / len(chips) // 8px padding each side
	chipH := rect.Dy() - 16                            // 8px padding top/bottom

	for idx, chip := range chips {
		x0 := rect.Min.X + 8 + idx*(chipW+gap)
		y0 := rect.Min.Y + 8
		chipRect := image.Rect(x0, y0, x0+chipW, y0+chipH)

		bgCol := parseHexColor(chip.BgColor)
		if chip.BgColor == "" {
			bgCol = color.RGBA{80, 80, 80, 255}
		}
		draw.Draw(img, chipRect, &image.Uniform{bgCol}, image.Point{}, draw.Src)

		// Auto-contrast: white on dark, black on light
		lum := 0.299*float64(bgCol.R) + 0.587*float64(bgCol.G) + 0.114*float64(bgCol.B)
		fgCol := color.RGBA{255, 255, 255, 255}
		if lum > 140 {
			fgCol = color.RGBA{0, 0, 0, 255}
		}

		ccx := chipRect.Min.X + chipRect.Dx()/2
		ccy := chipRect.Min.Y + chipRect.Dy()/2

		if chip.IconID != "" && chip.Label != "" {
			ui.DrawIcon(img, ccx, ccy-16, 36, chip.IconID, fgCol)
			ui.DrawText(img, ccx, ccy+24, 18, chip.Label, fgCol)
		} else if chip.IconID != "" {
			ui.DrawIcon(img, ccx, ccy, 42, chip.IconID, fgCol)
		} else if chip.Label != "" {
			ui.DrawText(img, ccx, ccy, 22, chip.Label, fgCol)
		}
	}
}

// drawLineGraph renders a time-series line graph within a button rectangle.
func drawLineGraph(img *image.RGBA, rect image.Rectangle, btn state.ButtonConfig, s state.ButtonState, textCol, text2Col color.RGBA) {
	w := rect.Dx()
	h := rect.Dy()

	// Get graph data - try current page first, then all-pages (-1)
	pageID := state.GlobalStore.CurrentPage
	values := state.GlobalStore.GetGraphValues(pageID, btn.ID)
	if len(values) == 0 {
		values = state.GlobalStore.GetGraphValues(-1, btn.ID)
	}

	// Graph color
	lineCol := color.RGBA{0, 255, 0, 255} // default green
	if s.GraphColor != "" {
		lineCol = parseHexColor(s.GraphColor)
	}

	// Y-axis range
	graphMin := btn.GraphMin
	graphMax := btn.GraphMax
	if graphMax <= graphMin {
		graphMax = 100
	}

	cx := rect.Min.X + w/2

	// Dark background fills entire button
	bgCol := color.RGBA{30, 30, 30, 255}
	draw.Draw(img, rect, &image.Uniform{bgCol}, image.Point{}, draw.Src)

	// Graph plotting area (inset from edges, leaving room for label at bottom)
	graphX := rect.Min.X + 10
	graphY := rect.Min.Y + 6
	graphW := w - 20
	graphH := h - 50
	if graphW < 10 || graphH < 10 {
		return
	}

	// Plot data points
	if len(values) >= 2 {
		n := len(values)
		for i := 0; i < n-1; i++ {
			x0 := graphX + (i * (graphW - 1) / (n - 1))
			x1 := graphX + ((i + 1) * (graphW - 1) / (n - 1))

			t0 := (values[i] - graphMin) / (graphMax - graphMin)
			t1 := (values[i+1] - graphMin) / (graphMax - graphMin)
			if t0 < 0 { t0 = 0 }
			if t0 > 1 { t0 = 1 }
			if t1 < 0 { t1 = 0 }
			if t1 > 1 { t1 = 1 }

			y0 := graphY + graphH - 1 - int(t0*float64(graphH-1))
			y1 := graphY + graphH - 1 - int(t1*float64(graphH-1))

			ui.DrawLineThick(img, x0, y0, x1, y1, 4, lineCol)
		}
	}

	// Value text overlaid on graph
	if s.Text != "" {
		ui.DrawText(img, cx, graphY+graphH/2, 28, s.Text, textCol)
	}

	// Label below graph
	if s.Text2 != "" {
		ui.DrawText(img, cx, rect.Min.Y+156, 28, s.Text2, text2Col)
	}
}

// drawGauge renders a gauge arc meter within a button rectangle.
func drawGauge(img *image.RGBA, rect image.Rectangle, btn state.ButtonConfig, s state.ButtonState, iconCol, textCol, text2Col color.RGBA) {
	w := rect.Dx()

	// Arc color
	fillCol := color.RGBA{0, 255, 0, 255} // default green
	if s.GraphColor != "" {
		fillCol = parseHexColor(s.GraphColor)
	}

	// Y-axis range
	graphMin := btn.GraphMin
	graphMax := btn.GraphMax
	if graphMax <= graphMin {
		graphMax = 100
	}

	// Get value: prefer graph_value property (template/lambda), fall back to push buffer
	var val float64
	if s.Value != "" {
		val, _ = strconv.ParseFloat(s.Value, 64)
	} else {
		pageID := state.GlobalStore.CurrentPage
		val, _ = state.GlobalStore.GetGraphLast(pageID, btn.ID)
	}

	// Compute fill fraction
	frac := (val - graphMin) / (graphMax - graphMin)
	if frac < 0 { frac = 0 }
	if frac > 1 { frac = 1 }

	// Arc geometry
	cx := rect.Min.X + w/2
	cy := rect.Min.Y + 85
	radius := 65
	thickness := 8

	// Arc sweep: 150deg to 390deg (240deg total, 120deg gap at bottom)
	startDeg := 150.0
	endDeg := 390.0
	totalSweep := endDeg - startDeg

	// Background arc (full sweep, dark gray)
	bgArcCol := color.RGBA{60, 60, 60, 255}
	ui.DrawArc(img, cx, cy, radius, startDeg, endDeg, thickness, bgArcCol)

	// Fill arc proportional to value
	if frac > 0 {
		fillEnd := startDeg + frac*totalSweep
		ui.DrawArc(img, cx, cy, radius, startDeg, fillEnd, thickness, fillCol)
	}

	// Icon and value text centered in arc
	if s.IconID != "" && s.Text != "" {
		ui.DrawIcon(img, cx, cy-16, 36, s.IconID, iconCol)
		ui.DrawText(img, cx, cy+18, 28, s.Text, textCol)
	} else if s.IconID != "" {
		ui.DrawIcon(img, cx, cy, 44, s.IconID, iconCol)
	} else if s.Text != "" {
		ui.DrawText(img, cx, cy, 28, s.Text, textCol)
	}

	// Label at bottom
	if s.Text2 != "" {
		ui.DrawText(img, cx, rect.Min.Y+156, 28, s.Text2, text2Col)
	}
}

// drawNotification renders the latest notification as word-wrapped multiline text
// that scrolls vertically from top to bottom, restarting when reaching the end.
func drawNotification(img *image.RGBA, rect image.Rectangle, btn state.ButtonConfig, s state.ButtonState, textCol, text2Col color.RGBA) {
	w := rect.Dx()
	h := rect.Dy()

	// Background color
	bgCol := parseHexColor(s.Color)
	if s.Color == "" {
		bgCol = color.RGBA{20, 20, 30, 255}
	}
	draw.Draw(img, rect, &image.Uniform{bgCol}, image.Point{}, draw.Src)

	// Clock in top-right corner
	clockStr := time.Now().Format("15:04")
	clockX := rect.Max.X - 40
	clockY := rect.Min.Y + 20
	ui.DrawIcon(img, clockX-50, clockY, 24, "clock-outline", text2Col)
	ui.DrawText(img, clockX, clockY, 22, clockStr, text2Col)

	notifications := btn.Notifications
	if len(notifications) == 0 {
		// No notifications - show empty state
		cx := rect.Min.X + w/2
		cy := rect.Min.Y + h/2
		ui.DrawIcon(img, cx, cy-20, 48, "bell-outline", color.RGBA{100, 100, 100, 255})
		ui.DrawText(img, cx, cy+30, 22, "No notifications", color.RGBA{150, 150, 150, 255})
		return
	}

	// Get the latest (last) notification
	latest := notifications[len(notifications)-1]
	notifCount := len(notifications)

	// Header area: bell icon + count
	headerY := rect.Min.Y + 25
	bellX := rect.Min.X + 30
	ui.DrawIcon(img, bellX, headerY, 32, "bell", textCol)

	// Notification count badge
	countStr := fmt.Sprintf("%d", notifCount)
	countX := rect.Min.X + 50
	countY := headerY - 8
	badgeCol := color.RGBA{255, 100, 100, 255}
	for dx := -12; dx <= 12; dx++ {
		for dy := -12; dy <= 12; dy++ {
			if dx*dx+dy*dy <= 144 { // radius 12
				img.Set(countX+dx, countY+dy, badgeCol)
			}
		}
	}
	ui.DrawText(img, countX, countY, 16, countStr, color.RGBA{255, 255, 255, 255})

	// Separator line
	lineY := rect.Min.Y + 55
	for x := rect.Min.X + 10; x < rect.Max.X-10; x++ {
		img.Set(x, lineY, color.RGBA{80, 80, 90, 255})
	}

	// Message area
	messageTop := rect.Min.Y + 60
	messageX := rect.Min.X + 15
	messageW := w - 30
	messageBottom := rect.Max.Y - 40

	// Timestamp at bottom-right
	timestamp := time.Unix(latest.Timestamp, 0)
	timeStr := timestamp.Format("15:04")
	ui.DrawTextLeft(img, rect.Max.X-60, rect.Max.Y-20, 18, timeStr, text2Col)

	// Word-wrap message into lines
	fontSize := 24.0
	lineHeight := 30
	lines := ui.WrapText(latest.Message, fontSize, messageW)

	// Clipping rect for the message area
	clipRect := image.Rect(rect.Min.X, messageTop, rect.Max.X, messageBottom)
	visibleH := messageBottom - messageTop
	totalH := len(lines) * lineHeight

	if totalH <= visibleH {
		// All lines fit — draw centered vertically, no scrolling
		startY := messageTop + (visibleH-totalH)/2 + lineHeight/2
		for i, line := range lines {
			y := startY + i*lineHeight
			ui.DrawTextLeftClipped(img, messageX, y, fontSize, line, textCol, clipRect)
		}
	} else {
		// Vertical scrolling: scroll offset is in pixels
		scrollOffset := btn.ScrollOffset
		startY := messageTop + lineHeight/2 - scrollOffset
		for i, line := range lines {
			y := startY + i*lineHeight
			// Only draw lines that are at least partially visible
			if y+lineHeight < messageTop || y-lineHeight > messageBottom {
				continue
			}
			ui.DrawTextLeftClipped(img, messageX, y, fontSize, line, textCol, clipRect)
		}
	}
}

// notificationMessageWidth returns the available text width for the notification widget at button index.
func notificationMessageWidth(btnIdx int) int {
	w := layout.BtnW
	if btnIdx == 13 {
		w = 2*w + layout.GapX // double-wide button
	}
	return w - 30 // 15px padding each side
}

// updateNotificationScrolls updates vertical scroll offsets for notification widgets.
// Text scrolls from top to bottom, pauses briefly, then restarts from the top.
func updateNotificationScrolls() {
	pageID := state.GlobalStore.CurrentPage

	for btnID := 0; btnID < 14; btnID++ {
		btn := state.GlobalStore.GetCurrentButton(btnID)
		if btn.WideLayout != "notification" || len(btn.Notifications) == 0 {
			continue
		}

		latest := btn.Notifications[len(btn.Notifications)-1]
		messageW := notificationMessageWidth(btnID)

		fontSize := 24.0
		lineHeight := 30
		lines := ui.WrapText(latest.Message, fontSize, messageW)
		visibleH := 240 - 100 // messageBottom - messageTop (approx)
		totalH := len(lines) * lineHeight

		if totalH <= visibleH {
			continue // no scrolling needed
		}

		maxScroll := totalH - visibleH
		newOffset := btn.ScrollOffset + 1 // 1px per tick (100ms ticker = smooth)

		if newOffset > maxScroll+60 { // pause ~60 ticks at bottom, then restart
			newOffset = 0
		}

		state.GlobalStore.UpdateScrollOffset(pageID, btnID, newOffset)
		markButtonDirty(btnID)
	}
}

func hexNibble(b byte) byte {
	switch {
	case b >= '0' && b <= '9':
		return b - '0'
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10
	}
	return 0
}

func hexByte(hi, lo byte) uint8 {
	return hexNibble(hi)<<4 | hexNibble(lo)
}

func parseHexColor(s string) color.RGBA {
	if len(s) == 7 && s[0] == '#' {
		return color.RGBA{
			R: hexByte(s[1], s[2]),
			G: hexByte(s[3], s[4]),
			B: hexByte(s[5], s[6]),
			A: 255,
		}
	}
	return color.RGBA{0, 0, 0, 255}
}

// applyStyleModulation modulates a color based on the button style.
// "constant" = no change, "fast" = rapid flash (~250ms), "slow" = breathing (~2s).
func applyStyleModulation(c color.RGBA, style string) color.RGBA {
	switch style {
	case "fast":
		// Toggle between 100% and 30% every 250ms
		phase := (time.Now().UnixMilli() / 250) % 2
		if phase == 1 {
			c.R = uint8(float64(c.R) * 0.3)
			c.G = uint8(float64(c.G) * 0.3)
			c.B = uint8(float64(c.B) * 0.3)
		}
	case "slow":
		// Sinusoidal breathing over ~2s cycle (range 0.3 .. 1.0)
		t := float64(time.Now().UnixMilli()) * 2.0 * math.Pi / 2000.0
		factor := 0.65 + 0.35*math.Sin(t)
		c.R = uint8(float64(c.R) * factor)
		c.G = uint8(float64(c.G) * factor)
		c.B = uint8(float64(c.B) * factor)
	}
	return c
}

