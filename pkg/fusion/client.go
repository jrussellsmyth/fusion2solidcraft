package fusion

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Client interfaces with Fusion 360's MCP HTTP endpoint.
type Client struct {
	BaseURL    string
	SessionID  string
	HTTPClient *http.Client
	mu         sync.Mutex
	reqID      int
}

// NewClient creates a new client pointing to the given MCP URL (typically http://127.0.0.1:27182/mcp).
func NewClient(baseURL string) *Client {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:27182/mcp"
	}
	return &Client{
		BaseURL: baseURL,
		HTTPClient: &http.Client{
			Timeout: 120 * time.Second,
		},
		reqID: 1,
	}
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      *int        `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type mcpToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type mcpToolCallResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// EnsureSession initializes an MCP session if one has not already been established.
func (c *Client) EnsureSession() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.SessionID != "" {
		return nil
	}

	reqID := c.reqID
	c.reqID++

	initReq := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &reqID,
		Method:  "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]interface{}{},
			"clientInfo": map[string]string{
				"name":    "fusion2solidcraft",
				"version": "1.0",
			},
		},
	}

	payload, err := json.Marshal(initReq)
	if err != nil {
		return err
	}

	httpReq, err := http.NewRequest("POST", c.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to POST initialize: %w", err)
	}
	defer resp.Body.Close()

	sid := resp.Header.Get("MCP-Session-Id")
	if sid == "" {
		sid = resp.Header.Get("Mcp-Session-Id")
	}
	if sid == "" {
		return fmt.Errorf("server did not return MCP-Session-Id header")
	}
	c.SessionID = sid

	// Send notifications/initialized as required by MCP spec
	notifReq := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	notifPayload, _ := json.Marshal(notifReq)
	notifHttpReq, err := http.NewRequest("POST", c.BaseURL, bytes.NewReader(notifPayload))
	if err == nil {
		notifHttpReq.Header.Set("Content-Type", "application/json")
		notifHttpReq.Header.Set("MCP-Session-Id", sid)
		if nResp, err := c.HTTPClient.Do(notifHttpReq); err == nil {
			nResp.Body.Close()
		}
	}

	return nil
}

// ExecuteScript runs an embedded Python script inside the active Fusion 360 session.
func (c *Client) ExecuteScript(script string, readOnly bool) (string, error) {
	if err := c.EnsureSession(); err != nil {
		return "", err
	}

	c.mu.Lock()
	currID := c.reqID
	c.reqID++
	sid := c.SessionID
	c.mu.Unlock()

	reqBody := jsonRPCRequest{
		JSONRPC: "2.0",
		ID:      &currID,
		Method:  "tools/call",
		Params: mcpToolCallParams{
			Name: "fusion_mcp_execute",
			Arguments: map[string]interface{}{
				"featureType": "script",
				"object": map[string]interface{}{
					"script":   script,
					"readOnly": readOnly,
				},
			},
		},
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest("POST", c.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("MCP-Session-Id", sid)

	resp, err := c.HTTPClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed to POST to Fusion MCP (%s): %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(bodyBytes, &rpcResp); err != nil {
		return "", fmt.Errorf("invalid JSON-RPC response: %w (%s)", err, string(bodyBytes))
	}

	if len(rpcResp.Error) > 0 && string(rpcResp.Error) != "null" {
		return "", fmt.Errorf("Fusion MCP error: %s", string(rpcResp.Error))
	}

	var toolResult mcpToolCallResult
	if err := json.Unmarshal(rpcResp.Result, &toolResult); err != nil {
		return "", fmt.Errorf("failed to unmarshal tool result: %w", err)
	}

	if len(toolResult.Content) == 0 {
		return "", fmt.Errorf("empty content from Fusion MCP")
	}

	text := toolResult.Content[0].Text
	if toolResult.IsError {
		return "", fmt.Errorf("script execution error: %s", text)
	}

	return text, nil
}
