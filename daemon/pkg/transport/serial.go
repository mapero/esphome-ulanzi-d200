package transport

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"

	"go.bug.st/serial"
)

// Config
const SERIAL_PORT = "/dev/ttyFIQ0"
const BAUD_RATE = 1500000 // Rockchip standard

type Message struct {
	Type    string      `json:"type"` // "event", "response", "batch_response"
	Payload interface{} `json:"payload"`
}

// SerialResponse wraps a Response with a type field for serial framing
type SerialResponse struct {
	Type    string `json:"type"`              // "response"
	Status  string `json:"status"`            // "ok", "error"
	Code    int    `json:"code,omitempty"`     // 0=success, 1=error, etc.
	Message string `json:"message,omitempty"` // Human-readable (errors only)
	Data    string `json:"data,omitempty"`     // Optional data
}

// SerialBatchResponse wraps multiple responses for batch commands
type SerialBatchResponse struct {
	Type      string        `json:"type"`      // "batch_response"
	Responses []Response `json:"responses"` // Individual responses
}

var port serial.Port
var portMutex sync.Mutex // Protect concurrent access

func Init() error {
	mode := &serial.Mode{
		BaudRate: BAUD_RATE,
	}
	var err error
	port, err = serial.Open(SERIAL_PORT, mode)
	if err != nil {
		return fmt.Errorf("serial open: %v", err)
	}

	log.Println("Transport: Serial initialized on", SERIAL_PORT)
	return nil
}

// sendRaw writes raw bytes to the serial port with mutex protection.
func sendRaw(data []byte) {
	if port == nil {
		return
	}

	portMutex.Lock()
	defer portMutex.Unlock()

	_, err := port.Write(data)
	if err != nil {
		log.Println("Transport: Write error:", err)
	}
}

// Send marshals a Message and writes it as NDJSON to serial.
func Send(msg Message) {
	if port == nil {
		return
	}

	data, err := json.Marshal(msg)
	if err != nil {
		log.Println("Transport: Marshal error:", err)
		return
	}
	data = append(data, '\n')

	sendRaw(data)
}

// sendSerialResponse writes a typed response to serial (for command responses).
func sendSerialResponse(resp Response) {
	sr := SerialResponse{
		Type:    "response",
		Status:  resp.Status,
		Code:    resp.Code,
		Message: resp.Message,
		Data:    resp.Data,
	}
	data, err := json.Marshal(sr)
	if err != nil {
		log.Println("Transport: Marshal response error:", err)
		return
	}
	data = append(data, '\n')

	sendRaw(data)
}

// sendSerialBatchResponse writes a batch response to serial.
func sendSerialBatchResponse(responses []Response) {
	br := SerialBatchResponse{
		Type:      "batch_response",
		Responses: responses,
	}
	data, err := json.Marshal(br)
	if err != nil {
		log.Println("Transport: Marshal batch response error:", err)
		return
	}
	data = append(data, '\n')

	sendRaw(data)
}

// StartSerialReader spawns a goroutine that reads NDJSON lines from serial
// and routes commands/batches to the command handler.
func StartSerialReader() {
	if port == nil {
		log.Println("Transport: Cannot start serial reader, port not initialized")
		return
	}

	go func() {
		scanner := bufio.NewScanner(port)
		scanner.Buffer(make([]byte, 0, MAX_MSG_SIZE), MAX_MSG_SIZE)

		log.Println("Transport: Serial reader started")

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}

			// Extract JSON from line (robust against kernel printk noise)
			jsonStart := strings.Index(line, "{")
			if jsonStart < 0 {
				continue // No JSON on this line
			}
			jsonEnd := strings.LastIndex(line, "}")
			if jsonEnd < 0 || jsonEnd <= jsonStart {
				continue
			}
			jsonStr := line[jsonStart : jsonEnd+1]

			// Peek at "type" field to route
			var peek struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(jsonStr), &peek); err != nil {
				log.Printf("Transport: Serial JSON parse error: %v (line: %s)", err, line)
				continue
			}

			switch peek.Type {
			case "command":
				handleSerialCommand(jsonStr)
			case "batch":
				handleSerialBatch(jsonStr)
			default:
				log.Printf("Transport: Serial unknown type %q: %s", peek.Type, jsonStr)
			}
		}

		if err := scanner.Err(); err != nil {
			log.Printf("Transport: Serial reader error: %v", err)
		}
		log.Println("Transport: Serial reader stopped")
	}()
}

// handleSerialCommand parses a single command from serial and sends the response.
func handleSerialCommand(jsonStr string) {
	msg, err := parseCommand(jsonStr)
	if err != nil {
		sendSerialResponse(Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Parse error: %v", err),
		})
		return
	}

	resp := handleCommand(msg)
	sendSerialResponse(resp)
}

// handleSerialBatch parses a batch of commands from serial and sends a batch response.
func handleSerialBatch(jsonStr string) {
	var batch BatchMessage
	if err := json.Unmarshal([]byte(jsonStr), &batch); err != nil {
		sendSerialResponse(Response{
			Status:  "error",
			Code:    CODE_INVALID,
			Message: fmt.Sprintf("Invalid batch JSON: %v", err),
		})
		return
	}

	results := make([]Response, 0, len(batch.Commands))
	for i, cmd := range batch.Commands {
		if cmd.Type == "" {
			cmd.Type = "command"
		}
		if cmd.Params == nil {
			cmd.Params = []string{}
		}

		if cmd.Module == "" || cmd.Action == "" {
			results = append(results, Response{
				Status:  "error",
				Code:    CODE_INVALID,
				Message: fmt.Sprintf("Command %d: missing module or action", i),
			})
			continue
		}

		resp := handleCommand(&cmd)
		results = append(results, resp)
	}

	sendSerialBatchResponse(results)
}
