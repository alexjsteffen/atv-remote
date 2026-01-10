package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const defaultPort = 8765

var (
	logger     = log.New(os.Stderr, "[server] ", log.LstdFlags)
	wsLogger   = log.New(os.Stdout, "[websocket] ", log.LstdFlags)
	upgrader   = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins for local use
		},
	}

	// Global state
	scanLookup     = make(map[string]string) // txt -> identifier
	pairingATV     = ""
	activePairing  = false
	activeDeviceID = ""
	activeWS       *websocket.Conn
	pairingCreds   = make(map[string]interface{})
	keepRunning    = true
	pyHelper       *exec.Cmd
	pyStdin        io.WriteCloser
	pyStdout       *bufio.Scanner
	pyMutex        sync.Mutex
)

// Message represents incoming WebSocket messages
type Message struct {
	Cmd  string      `json:"cmd"`
	Data interface{} `json:"data,omitempty"`
}

// Response represents outgoing WebSocket messages
type Response struct {
	Command string      `json:"command"`
	Data    interface{} `json:"data"`
}

// PyRequest is a request to the Python helper
type PyRequest struct {
	Cmd  string      `json:"cmd"`
	Data interface{} `json:"data,omitempty"`
}

// PyResponse is a response from the Python helper
type PyResponse struct {
	Success bool        `json:"success"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func sendCommand(ws *websocket.Conn, command string, data interface{}) error {
	if ws == nil {
		return fmt.Errorf("websocket is nil")
	}
	resp := Response{
		Command: command,
		Data:    data,
	}
	if resp.Data == nil {
		resp.Data = []interface{}{}
	}
	return ws.WriteJSON(resp)
}

func getPyHelperPath() string {
	execPath, err := os.Executable()
	if err != nil {
		return "pyatv_helper.py"
	}
	return filepath.Join(filepath.Dir(execPath), "pyatv_helper.py")
}

func startPyHelper() error {
	pyMutex.Lock()
	defer pyMutex.Unlock()

	helperPath := getPyHelperPath()
	logger.Printf("Starting Python helper: %s", helperPath)

	// Find Python executable
	pythonCmd := "python3"
	if _, err := exec.LookPath("python3"); err != nil {
		pythonCmd = "python"
	}

	pyHelper = exec.Command(pythonCmd, helperPath)
	pyHelper.Stderr = os.Stderr

	var err error
	pyStdin, err = pyHelper.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := pyHelper.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	pyStdout = bufio.NewScanner(stdout)

	if err := pyHelper.Start(); err != nil {
		return fmt.Errorf("failed to start Python helper: %w", err)
	}

	logger.Printf("Python helper started with PID %d", pyHelper.Process.Pid)
	return nil
}

func stopPyHelper() {
	pyMutex.Lock()
	defer pyMutex.Unlock()

	if pyHelper != nil && pyHelper.Process != nil {
		logger.Printf("Stopping Python helper")
		pyStdin.Close()
		pyHelper.Process.Kill()
		pyHelper.Wait()
		pyHelper = nil
	}
}

func callPyHelper(cmd string, data interface{}) (*PyResponse, error) {
	pyMutex.Lock()
	defer pyMutex.Unlock()

	if pyHelper == nil {
		return nil, fmt.Errorf("Python helper not running")
	}

	req := PyRequest{Cmd: cmd, Data: data}
	reqBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Send request
	_, err = pyStdin.Write(append(reqBytes, '\n'))
	if err != nil {
		return nil, fmt.Errorf("failed to write to Python helper: %w", err)
	}

	// Read response
	if !pyStdout.Scan() {
		if err := pyStdout.Err(); err != nil {
			return nil, fmt.Errorf("failed to read from Python helper: %w", err)
		}
		return nil, fmt.Errorf("Python helper closed unexpectedly")
	}

	var resp PyResponse
	if err := json.Unmarshal(pyStdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &resp, nil
}

func parseRequest(msg Message, ws *websocket.Conn) error {
	activeWS = ws
	cmd := msg.Cmd

	switch cmd {
	case "quit":
		logger.Printf("quit command received")
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)

	case "scan":
		resp, err := callPyHelper("scan", nil)
		if err != nil {
			logger.Printf("scan error: %v", err)
			return sendCommand(ws, "scanResult", []string{})
		}
		if !resp.Success {
			logger.Printf("scan failed: %s", resp.Error)
			return sendCommand(ws, "scanResult", []string{})
		}

		// Parse scan results and build lookup
		if devices, ok := resp.Data.([]interface{}); ok {
			results := make([]string, 0)
			scanLookup = make(map[string]string)
			for _, d := range devices {
				if dev, ok := d.(map[string]interface{}); ok {
					name := dev["name"].(string)
					address := dev["address"].(string)
					identifier := dev["identifier"].(string)
					txt := fmt.Sprintf("%s (%s)", name, address)
					results = append(results, txt)
					scanLookup[txt] = identifier
				}
			}
			return sendCommand(ws, "scanResult", results)
		}
		return sendCommand(ws, "scanResult", []string{})

	case "echo":
		return sendCommand(ws, "echo_reply", msg.Data)

	case "startPair":
		deviceTxt, ok := msg.Data.(string)
		if !ok {
			return sendCommand(ws, "pairError", "Invalid device selection")
		}

		identifier, ok := scanLookup[deviceTxt]
		if !ok {
			return sendCommand(ws, "pairError", "Device not found in scan results")
		}

		pairingATV = identifier
		logger.Printf("Starting pairing for device: %s", identifier)

		resp, err := callPyHelper("startPair", map[string]string{"identifier": identifier})
		if err != nil {
			logger.Printf("startPair error: %v", err)
			return sendCommand(ws, "pairError", err.Error())
		}
		if !resp.Success {
			logger.Printf("startPair failed: %s", resp.Error)
			return sendCommand(ws, "pairError", resp.Error)
		}
		activePairing = true

	case "finishPair1":
		pin, ok := msg.Data.(string)
		if !ok {
			if pinFloat, ok := msg.Data.(float64); ok {
				pin = strconv.Itoa(int(pinFloat))
			} else {
				return sendCommand(ws, "pairError", "Invalid PIN")
			}
		}
		logger.Printf("finishPair1 with PIN: %s", pin)

		resp, err := callPyHelper("finishPair1", map[string]string{"pin": pin, "identifier": pairingATV})
		if err != nil {
			logger.Printf("finishPair1 error: %v", err)
			return sendCommand(ws, "pairError", err.Error())
		}
		if !resp.Success {
			logger.Printf("finishPair1 failed: %s", resp.Error)
			return sendCommand(ws, "pairError", resp.Error)
		}

		// Store credentials and start companion pairing
		if data, ok := resp.Data.(map[string]interface{}); ok {
			pairingCreds["credentials"] = data["credentials"]
			pairingCreds["identifier"] = data["identifier"]
		}
		return sendCommand(ws, "startPair2", nil)

	case "finishPair2":
		pin, ok := msg.Data.(string)
		if !ok {
			if pinFloat, ok := msg.Data.(float64); ok {
				pin = strconv.Itoa(int(pinFloat))
			} else {
				return sendCommand(ws, "pairError", "Invalid PIN")
			}
		}
		logger.Printf("finishPair2 with PIN: %s", pin)

		resp, err := callPyHelper("finishPair2", map[string]string{"pin": pin, "identifier": pairingATV})
		if err != nil {
			logger.Printf("finishPair2 error: %v", err)
			return sendCommand(ws, "pairError", err.Error())
		}
		if !resp.Success {
			logger.Printf("finishPair2 failed: %s", resp.Error)
			return sendCommand(ws, "pairError", resp.Error)
		}

		if data, ok := resp.Data.(map[string]interface{}); ok {
			pairingCreds["Companion"] = data["credentials"]
		}
		activePairing = false
		return sendCommand(ws, "pairCredentials", pairingCreds)

	case "finishPair":
		pin, ok := msg.Data.(string)
		if !ok {
			if pinFloat, ok := msg.Data.(float64); ok {
				pin = strconv.Itoa(int(pinFloat))
			} else {
				return sendCommand(ws, "pairError", "Invalid PIN")
			}
		}
		logger.Printf("finishPair with PIN: %s", pin)

		resp, err := callPyHelper("finishPair", map[string]string{"pin": pin, "identifier": pairingATV})
		if err != nil {
			logger.Printf("finishPair error: %v", err)
			return sendCommand(ws, "pairError", err.Error())
		}
		if !resp.Success {
			logger.Printf("finishPair failed: %s", resp.Error)
			return sendCommand(ws, "pairError", resp.Error)
		}

		if data, ok := resp.Data.(map[string]interface{}); ok {
			result := map[string]interface{}{
				"credentials": data["credentials"],
				"identifier":  data["identifier"],
			}
			activePairing = false
			return sendCommand(ws, "pairCredentials", result)
		}

	case "connect":
		data, ok := msg.Data.(map[string]interface{})
		if !ok {
			return sendCommand(ws, "connected", map[string]interface{}{"connected": false, "error": "Invalid connection data"})
		}

		resp, err := callPyHelper("connect", data)
		if err != nil {
			logger.Printf("connect error: %v", err)
			return sendCommand(ws, "connected", map[string]interface{}{"connected": false, "error": err.Error()})
		}
		if !resp.Success {
			logger.Printf("connect failed: %s", resp.Error)
			return sendCommand(ws, "connected", map[string]interface{}{"connected": false, "error": resp.Error})
		}

		if connData, ok := resp.Data.(map[string]interface{}); ok {
			activeDeviceID = data["identifier"].(string)
			if err := sendCommand(ws, "connected", connData); err != nil {
				return err
			}

			// Get power status after connecting
			if powerStatus, ok := connData["power_status"].(string); ok {
				return sendCommand(ws, "power_status", powerStatus)
			}
		}

	case "kbfocus":
		if activeDeviceID == "" {
			return nil
		}

		resp, err := callPyHelper("kbfocus", nil)
		if err != nil {
			logger.Printf("kbfocus error: %v", err)
			return nil
		}
		if resp.Success {
			if status, ok := resp.Data.(string); ok {
				return sendCommand(ws, "kbfocus-status", status)
			}
		}

	case "settext":
		data, ok := msg.Data.(map[string]interface{})
		if !ok {
			return nil
		}
		if activeDeviceID == "" {
			return nil
		}

		_, err := callPyHelper("settext", data)
		if err != nil {
			logger.Printf("settext error: %v", err)
		}

	case "gettext":
		if activeDeviceID == "" {
			return nil
		}

		resp, err := callPyHelper("gettext", nil)
		if err != nil {
			logger.Printf("gettext error: %v", err)
			return nil
		}
		if resp.Success {
			return sendCommand(ws, "current-text", resp.Data)
		}

	case "key":
		if activeDeviceID == "" {
			return nil
		}

		resp, err := callPyHelper("key", msg.Data)
		if err != nil {
			logger.Printf("key error: %v", err)
		}
		if resp != nil && !resp.Success {
			logger.Printf("key failed: %s", resp.Error)
		}

	case "power_status":
		if activeDeviceID == "" {
			return nil
		}

		resp, err := callPyHelper("power_status", nil)
		if err != nil {
			logger.Printf("power_status error: %v", err)
			return sendCommand(ws, "power_error", err.Error())
		}
		if !resp.Success {
			return sendCommand(ws, "power_error", resp.Error)
		}
		return sendCommand(ws, "power_status", resp.Data)

	case "power_toggle":
		if activeDeviceID == "" {
			return nil
		}

		resp, err := callPyHelper("power_toggle", nil)
		if err != nil {
			logger.Printf("power_toggle error: %v", err)
			return sendCommand(ws, "power_error", err.Error())
		}
		if !resp.Success {
			return sendCommand(ws, "power_error", resp.Error)
		}
		return sendCommand(ws, "power_status", resp.Data)
	}

	return nil
}

func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer ws.Close()

	wsLogger.Printf("Client connected from %s", r.RemoteAddr)

	for {
		_, message, err := ws.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Printf("WebSocket error: %v", err)
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(message, &msg); err != nil {
			logger.Printf("Error parsing message: %v, message: %s", err, message)
			continue
		}

		if err := parseRequest(msg, ws); err != nil {
			logger.Printf("Error processing request: %v", err)
			sendCommand(ws, "error", err.Error())
		}
	}

	wsLogger.Printf("Client disconnected")
}

func checkExitFile() {
	// Remove stopserver file if it exists at startup
	if _, err := os.Stat("stopserver"); err == nil {
		os.Remove("stopserver")
	}

	for keepRunning {
		time.Sleep(500 * time.Millisecond)
		if _, err := os.Stat("stopserver"); err == nil {
			logger.Printf("stopserver file found, exiting")
			os.Remove("stopserver")
			os.Exit(0)
		}
	}
}

func main() {
	port := flag.Int("port", defaultPort, "Port to listen on")
	flag.Parse()

	// Handle command line args without flags
	if flag.NArg() > 0 {
		if arg := flag.Arg(0); arg == "-h" || arg == "--help" || arg == "-?" || arg == "/?" {
			fmt.Printf("Usage: wsserver [port]\n\nPort number by default is %d\n", defaultPort)
			os.Exit(0)
		}
		if p, err := strconv.Atoi(flag.Arg(0)); err == nil {
			*port = p
		}
	}

	// Print banner
	width := 80
	txt := fmt.Sprintf("wsserver WebSocket - ATV Server (Go)")
	logger.Printf(strings.Repeat("=", width))
	logger.Printf("%s", strings.Repeat(" ", (width-len(txt))/2)+txt)
	logger.Printf(strings.Repeat("=", width))

	// Start Python helper
	if err := startPyHelper(); err != nil {
		logger.Printf("Failed to start Python helper: %v", err)
		logger.Printf("Note: Ensure pyatv_helper.py is in the same directory as this binary")
		os.Exit(1)
	}
	defer stopPyHelper()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		logger.Printf("Received shutdown signal")
		keepRunning = false
		stopPyHelper()
		os.Exit(0)
	}()

	// Start exit file checker
	go checkExitFile()

	// Set up HTTP handler
	http.HandleFunc("/", handleWebSocket)

	addr := fmt.Sprintf("localhost:%d", *port)
	wsLogger.Printf("server listening on ws://%s", addr)

	if err := http.ListenAndServe(addr, nil); err != nil {
		logger.Printf("Server error: %v", err)
		os.Exit(1)
	}
}
