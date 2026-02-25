package state

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const STATE_FILE = "config/state.json"

type Chip struct {
	IconID  string `json:"icon_id"`
	Label   string `json:"label"`
	BgColor string `json:"bg_color"`
}

type Notification struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
}

const MaxNotifications = 50

type ButtonState struct {
	Color      string `json:"color"`
	Text       string `json:"text"`
	TextColor  string `json:"text_color"`
	Text2      string `json:"text2"`       // Secondary
	Text2Color string `json:"text2_color"` // Secondary Color
	IconID     string `json:"icon_id"`
	IconColor  string `json:"icon_color"`
	Style      string `json:"style"`       // "constant" (default), "fast", "slow"
	Chips      []Chip `json:"chips,omitempty"` // for "chips" layout
	GraphColor string `json:"graph_color,omitempty"` // line/arc color hex for graph visualizations
	Value string `json:"value,omitempty"` // current numeric value for gauge rendering
}

type ButtonConfig struct {
	ID            int                    `json:"id"`
	Label         string                 `json:"label"`
	Type          string                 `json:"type"`          // "momentary", "toggle", "page-next", "page-prev", "page-jump"
	TargetPage    int                    `json:"target_page"`   // For page-jump
	CurrentState  string                 `json:"current_state"` // "default", "active", "pressed"
	States        map[string]ButtonState `json:"states"`
	WideLayout    string                 `json:"wide_layout,omitempty"`  // "side-by-side", "status", "chips", "line-graph", "gauge", "notification"
	StatusItems   []string               `json:"status_items,omitempty"` // e.g. ["clock", "page"]
	GraphSize     int                    `json:"graph_size,omitempty"`   // ring buffer capacity (default 20)
	GraphMin      float64                `json:"graph_min,omitempty"`    // Y-axis min (default 0)
	GraphMax      float64                `json:"graph_max,omitempty"`    // Y-axis max (default 100)
	Notifications []Notification         `json:"notifications,omitempty"` // list of notifications
	ScrollOffset  int                    `json:"-"`                       // transient scroll position, never persisted
}

type Page struct {
	ID      int                     `json:"id"`
	Label   string                  `json:"label"`
	Buttons map[int]ButtonConfig    `json:"buttons"`
}

type Store struct {
	mu           sync.RWMutex
	Pages        map[int]Page `json:"pages"`
	CurrentPage  int          `json:"current_page"`
	graphBuffers map[string]*RingBuffer // key = "pageID:buttonID", transient
	saveCh       chan struct{}           // debounced save signal
}

var GlobalStore = &Store{
	Pages: make(map[int]Page),
}

func Init() {
	GlobalStore.mu.Lock()
	defer GlobalStore.mu.Unlock()

	GlobalStore.graphBuffers = make(map[string]*RingBuffer)
	GlobalStore.saveCh = make(chan struct{}, 1)
	go GlobalStore.saveLoop()

	if err := GlobalStore.load(); err == nil {
		if len(GlobalStore.Pages) == 0 {
			createDefaultPage(0)
		}
		fmt.Println("State loaded.")
		return
	}
	
	createDefaultPage(0)
}

func createDefaultPage(id int) {
	createDefaultPageWithLabel(id, fmt.Sprintf("Page %d", id+1))
}

func createDefaultPageWithLabel(id int, label string) {
	btns := make(map[int]ButtonConfig)
	for i := 0; i < 14; i++ {
		btns[i] = ButtonConfig{
			ID:           i,
			Label:        fmt.Sprintf("Btn %d", i),
			Type:         "momentary",
			CurrentState: "default",
			States: map[string]ButtonState{
				"default": {Color: "#333333", Text: "", TextColor: "#FFFFFF", IconColor: "#FFFFFF"},
				"active":  {Color: "#FFFFFF", Text: "", TextColor: "#000000", IconColor: "#000000"},
				"pressed": {Color: "#00FF00", Text: "", TextColor: "#000000", IconColor: "#000000"},
			},
		}
	}
	GlobalStore.Pages[id] = Page{ID: id, Label: label, Buttons: btns}
	GlobalStore.CurrentPage = id
}

func (s *Store) GetCurrentButtons() map[int]ButtonConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.Pages[s.CurrentPage]; ok {
		return p.Buttons
	}
	return nil
}

func (s *Store) GetButton(pageID, btnID int) ButtonConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getButton(pageID, btnID)
}

func (s *Store) GetCurrentButton(btnID int) ButtonConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.getButton(s.CurrentPage, btnID)
}

// getButton is an internal helper that doesn't lock (caller must hold lock)
func (s *Store) getButton(pageID, btnID int) ButtonConfig {
	if p, ok := s.Pages[pageID]; ok {
		if b, ok := p.Buttons[btnID]; ok {
			return b
		}
	}
	return ButtonConfig{}
}

func (s *Store) SetButtonState(pageID, btnID int, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.Pages[pageID]; ok {
		if b, ok := p.Buttons[btnID]; ok {
			b.CurrentState = state
			p.Buttons[btnID] = b
			s.Pages[pageID] = p
		}
	}
	s.requestSave()
}

func (s *Store) SetCurrentButtonState(btnID int, state string) {
	s.mu.RLock()
	pageID := s.CurrentPage
	s.mu.RUnlock()
	s.SetButtonState(pageID, btnID, state)
}

func (s *Store) UpdateButton(pageID, btnID int, config ButtonConfig) {
	s.UpdateButtonWithPersist(pageID, btnID, config, true)
}

func (s *Store) UpdateButtonWithPersist(pageID, btnID int, config ButtonConfig, persist bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Pages[pageID]
	if !ok {
		// Auto-create the page with default buttons
		p = Page{ID: pageID, Label: fmt.Sprintf("Page %d", pageID+1), Buttons: make(map[int]ButtonConfig)}
		for i := 0; i < 14; i++ {
			p.Buttons[i] = ButtonConfig{
				ID: i, Label: fmt.Sprintf("Btn %d", i), Type: "momentary", CurrentState: "default",
				States: map[string]ButtonState{
					"default": {Color: "#333333", TextColor: "#FFFFFF", IconColor: "#FFFFFF"},
					"active":  {Color: "#FFFFFF", TextColor: "#000000", IconColor: "#000000"},
					"pressed": {Color: "#00FF00", TextColor: "#000000", IconColor: "#000000"},
				},
			}
		}
		log.Printf("Auto-created page %d", pageID)
	}
	// Preserve existing notifications and scroll offset
	if existing, ok := p.Buttons[btnID]; ok {
		config.Notifications = existing.Notifications
		config.ScrollOffset = existing.ScrollOffset
	}
	p.Buttons[btnID] = config
	s.Pages[pageID] = p
	if persist {
		s.requestSave()
	}
}

// ResetAllButtons resets all button slots on all pages to defaults.
// Called when ESP32 reconnects so stale configs are cleared.
func (s *Store) ResetAllButtons() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphBuffers = make(map[string]*RingBuffer)
	for id, page := range s.Pages {
		btns := make(map[int]ButtonConfig)
		for i := 0; i < 14; i++ {
			btns[i] = ButtonConfig{
				ID:           i,
				Label:        fmt.Sprintf("Btn %d", i),
				Type:         "momentary",
				CurrentState: "default",
				States: map[string]ButtonState{
					"default": {Color: "#333333", Text: "", TextColor: "#FFFFFF", IconColor: "#FFFFFF"},
					"active":  {Color: "#FFFFFF", Text: "", TextColor: "#000000", IconColor: "#000000"},
					"pressed": {Color: "#00FF00", Text: "", TextColor: "#000000", IconColor: "#000000"},
				},
			}
		}
		page.Buttons = btns
		s.Pages[id] = page
	}
}

func (s *Store) SetPage(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.Pages[id]; ok {
		s.CurrentPage = id
	}
	s.requestSave()
}

func (s *Store) AddPage() int {
	return s.AddPageWithLabel("")
}

func (s *Store) AddPageWithLabel(label string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	max := 0
	for k := range s.Pages {
		if k > max { max = k }
	}
	newID := max + 1

	if label == "" {
		label = fmt.Sprintf("Page %d", newID+1)
	}
	createDefaultPageWithLabel(newID, label)
	s.requestSave()
	return newID
}

type PageInfo struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
}

func (s *Store) GetPagesList() []int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []int
	for k := range s.Pages {
		list = append(list, k)
	}
	return list
}

func (s *Store) GetPagesInfo() []PageInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []PageInfo
	for _, p := range s.Pages {
		list = append(list, PageInfo{ID: p.ID, Label: p.Label})
	}
	return list
}

func (s *Store) DeletePage(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	
	if id == 0 { return fmt.Errorf("cannot delete default page") }
	if _, ok := s.Pages[id]; !ok { return fmt.Errorf("page not found") }
	
	delete(s.Pages, id)
	
	if s.CurrentPage == id {
		s.CurrentPage = 0
	}
	
	s.requestSave()
	return nil
}

func (s *Store) SetPageLabel(id int, label string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.Pages[id]
	if !ok {
		return fmt.Errorf("page not found")
	}
	p.Label = label
	s.Pages[id] = p
	s.requestSave()
	return nil
}

func (s *Store) GetPageLabel(id int) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.Pages[id]; ok {
		return p.Label
	}
	return ""
}

func (s *Store) GetPageByLabel(label string) (int, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for id, p := range s.Pages {
		if p.Label == label {
			return id, true
		}
	}
	return 0, false
}

func (s *Store) load() error {
	file, err := os.ReadFile(STATE_FILE)
	if err != nil { return err }
	return json.Unmarshal(file, s)
}

// requestSave signals the save loop to persist state.
// Multiple rapid calls are coalesced into a single write.
func (s *Store) requestSave() {
	select {
	case s.saveCh <- struct{}{}:
	default: // already pending
	}
}

// saveLoop runs as a single goroutine, debouncing save requests.
func (s *Store) saveLoop() {
	for range s.saveCh {
		// Debounce: wait briefly to coalesce rapid-fire changes
		time.Sleep(100 * time.Millisecond)
		// Drain any signals that arrived during the sleep
		for {
			select {
			case <-s.saveCh:
			default:
				goto doSave
			}
		}
	doSave:
		s.mu.RLock()
		data, err := json.Marshal(&struct {
			Pages       map[int]Page `json:"pages"`
			CurrentPage int          `json:"current_page"`
		}{
			Pages:       s.Pages,
			CurrentPage: s.CurrentPage,
		})
		s.mu.RUnlock()
		if err != nil {
			log.Printf("state: marshal error: %v", err)
			continue
		}
		if err := os.WriteFile(STATE_FILE, data, 0644); err != nil {
			log.Printf("state: write error: %v", err)
		}
	}
}

// --- RingBuffer ---

// RingBuffer is a fixed-capacity circular buffer of float64 values.
type RingBuffer struct {
	data  []float64
	cap   int
	head  int // next write position
	count int
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity < 2 {
		capacity = 2
	}
	return &RingBuffer{
		data: make([]float64, capacity),
		cap:  capacity,
	}
}

// Push adds a value to the ring buffer.
func (rb *RingBuffer) Push(val float64) {
	rb.data[rb.head] = val
	rb.head = (rb.head + 1) % rb.cap
	if rb.count < rb.cap {
		rb.count++
	}
}

// Values returns all values in chronological order (oldest first).
func (rb *RingBuffer) Values() []float64 {
	if rb.count == 0 {
		return nil
	}
	result := make([]float64, rb.count)
	start := (rb.head - rb.count + rb.cap) % rb.cap
	for i := 0; i < rb.count; i++ {
		result[i] = rb.data[(start+i)%rb.cap]
	}
	return result
}

// Last returns the most recently pushed value.
func (rb *RingBuffer) Last() (float64, bool) {
	if rb.count == 0 {
		return 0, false
	}
	idx := (rb.head - 1 + rb.cap) % rb.cap
	return rb.data[idx], true
}

// --- Graph buffer methods on Store ---

func graphKey(pageID, btnID int) string {
	return fmt.Sprintf("%d:%d", pageID, btnID)
}

// PushGraphValue pushes a sensor value into the ring buffer for a button.
func (s *Store) PushGraphValue(pageID, btnID int, value float64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := graphKey(pageID, btnID)
	rb, ok := s.graphBuffers[key]
	if !ok {
		// Determine capacity from button config
		cap := 20
		if p, pok := s.Pages[pageID]; pok {
			if b, bok := p.Buttons[btnID]; bok && b.GraphSize > 0 {
				cap = b.GraphSize
			}
		}
		rb = NewRingBuffer(cap)
		s.graphBuffers[key] = rb
	}
	rb.Push(value)
}

// GetGraphValues returns all buffered values for a button in chronological order.
func (s *Store) GetGraphValues(pageID, btnID int) []float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := graphKey(pageID, btnID)
	rb, ok := s.graphBuffers[key]
	if !ok {
		return nil
	}
	return rb.Values()
}

// GetGraphLast returns the most recent value for a button.
func (s *Store) GetGraphLast(pageID, btnID int) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	key := graphKey(pageID, btnID)
	rb, ok := s.graphBuffers[key]
	if !ok {
		return 0, false
	}
	return rb.Last()
}

// --- Notification methods ---

// addNotificationToPage adds a notification to a specific page's button (caller must hold lock).
func (s *Store) addNotificationToPage(pageID, btnID int, notification Notification) {
	p, ok := s.Pages[pageID]
	if !ok {
		return
	}
	b, ok := p.Buttons[btnID]
	if !ok {
		return
	}

	b.Notifications = append(b.Notifications, notification)

	// Cap at MaxNotifications, drop oldest
	if len(b.Notifications) > MaxNotifications {
		b.Notifications = b.Notifications[len(b.Notifications)-MaxNotifications:]
	}

	// Reset scroll offset when new notification arrives
	b.ScrollOffset = 0

	p.Buttons[btnID] = b
	s.Pages[pageID] = p
}

// AddNotification adds a new notification to a button.
// pageID -1 means add to all pages (for global widgets).
func (s *Store) AddNotification(pageID, btnID int, notification Notification) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if pageID == -1 {
		for pid := range s.Pages {
			s.addNotificationToPage(pid, btnID, notification)
		}
	} else {
		s.addNotificationToPage(pageID, btnID, notification)
	}
	s.requestSave()
}

// ClearNotification removes a notification from a button by ID.
// pageID -1 means clear from all pages.
func (s *Store) ClearNotification(pageID, btnID int, notificationID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	clearOnPage := func(pid int) {
		p, ok := s.Pages[pid]
		if !ok {
			return
		}
		b, ok := p.Buttons[btnID]
		if !ok {
			return
		}
		filtered := make([]Notification, 0, len(b.Notifications))
		for _, n := range b.Notifications {
			if n.ID != notificationID {
				filtered = append(filtered, n)
			}
		}
		b.Notifications = filtered
		b.ScrollOffset = 0
		p.Buttons[btnID] = b
		s.Pages[pid] = p
	}

	if pageID == -1 {
		for pid := range s.Pages {
			clearOnPage(pid)
		}
	} else {
		clearOnPage(pageID)
	}
	s.requestSave()
}

// ClearAllNotifications removes all notifications from a button.
// pageID -1 means clear from all pages.
func (s *Store) ClearAllNotifications(pageID, btnID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	clearOnPage := func(pid int) {
		p, ok := s.Pages[pid]
		if !ok {
			return
		}
		b, ok := p.Buttons[btnID]
		if !ok {
			return
		}
		b.Notifications = nil
		b.ScrollOffset = 0
		p.Buttons[btnID] = b
		s.Pages[pid] = p
	}

	if pageID == -1 {
		for pid := range s.Pages {
			clearOnPage(pid)
		}
	} else {
		clearOnPage(pageID)
	}
	s.requestSave()
}

// GetNotifications returns all notifications for a button.
// pageID -1 returns notifications from the current page.
func (s *Store) GetNotifications(pageID, btnID int) []Notification {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if pageID == -1 {
		pageID = s.CurrentPage
	}
	b := s.getButton(pageID, btnID)
	return b.Notifications
}

// UpdateScrollOffset updates the scroll offset for a notification widget.
func (s *Store) UpdateScrollOffset(pageID, btnID int, offset int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	updateOnPage := func(pid int) {
		p, ok := s.Pages[pid]
		if !ok {
			return
		}
		b, ok := p.Buttons[btnID]
		if !ok {
			return
		}
		b.ScrollOffset = offset
		p.Buttons[btnID] = b
		s.Pages[pid] = p
	}

	if pageID == -1 {
		for pid := range s.Pages {
			updateOnPage(pid)
		}
	} else {
		updateOnPage(pageID)
	}
}
