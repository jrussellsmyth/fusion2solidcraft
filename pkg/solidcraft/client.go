package solidcraft

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/jrussell/fusion2solidcraft/pkg/cadir"
)

// Client drives SolidCraft via solidcraft-cli mcp over stdin/stdout JSON-RPC.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	mu     sync.Mutex
	reqID  int
}

type mcpRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
}

type mcpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

type toolCallContent struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError,omitempty"`
}

// Start launches solidcraft-cli mcp and completes protocol handshake.
func Start() (*Client, error) {
	cmd := exec.Command("solidcraft-cli", "mcp", "--allow-any-path")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start solidcraft-cli mcp: %w", err)
	}

	client := &Client{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdoutPipe),
		reqID:  1,
	}

	// Initialize MCP
	initReq := mcpRequest{
		JSONRPC: "2.0",
		ID:      client.reqID,
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
	client.reqID++

	if err := client.write(initReq); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to write initialize: %w", err)
	}

	if _, err := client.read(); err != nil {
		client.Close()
		return nil, fmt.Errorf("failed to read initialize response: %w", err)
	}

	return client, nil
}

// Close terminates the solidcraft-cli process.
func (c *Client) Close() error {
	if c.stdin != nil {
		c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		return c.cmd.Process.Kill()
	}
	return nil
}

func (c *Client) write(req mcpRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = c.stdin.Write(b)
	return err
}

func (c *Client) read() (*mcpResponse, error) {
	line, err := c.stdout.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var resp mcpResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("invalid json response (%s): %w", string(line), err)
	}

	if resp.Error != nil {
		return nil, fmt.Errorf("solidcraft error (%d): %s", resp.Error.Code, resp.Error.Message)
	}

	return &resp, nil
}

// CallTool invokes an MCP tool on SolidCraft.
func (c *Client) CallTool(name string, args map[string]interface{}) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	req := mcpRequest{
		JSONRPC: "2.0",
		ID:      c.reqID,
		Method:  "tools/call",
		Params: map[string]interface{}{
			"name":      name,
			"arguments": args,
		},
	}
	c.reqID++

	if err := c.write(req); err != nil {
		return "", err
	}

	resp, err := c.read()
	if err != nil {
		return "", err
	}

	var content toolCallContent
	if err := json.Unmarshal(resp.Result, &content); err != nil {
		return "", fmt.Errorf("failed to unmarshal content: %w", err)
	}

	if len(content.Content) == 0 {
		return "", nil
	}

	text := content.Content[0].Text
	if content.IsError {
		if strings.Contains(text, "still running:") {
			for attempt := 0; attempt < 2; attempt++ {
				pReq := mcpRequest{
					JSONRPC: "2.0",
					ID:      c.reqID,
					Method:  "tools/call",
					Params: map[string]interface{}{
						"name":      "pending",
						"arguments": map[string]interface{}{},
					},
				}
				c.reqID++
				if wErr := c.write(pReq); wErr == nil {
					if pResp, rErr := c.read(); rErr == nil {
						var pContent toolCallContent
						if json.Unmarshal(pResp.Result, &pContent) == nil && len(pContent.Content) > 0 {
							pText := pContent.Content[0].Text
							if !strings.Contains(pText, `"done":false`) && !strings.Contains(pText, `"done": false`) {
								break
							}
						}
					}
				}
			}
		}
		return "", fmt.Errorf("tool error: %s", text)
	}

	return text, nil
}

// NewDesign resets the current workspace to a fresh empty document.
func (c *Client) NewDesign() error {
	_, err := c.CallTool("new_design", map[string]interface{}{})
	return err
}

// Batch executes a sequence of SolidCraft commands in one atomic call.
func (c *Client) Batch(commands []Command) (string, error) {
	return c.CallTool("batch", map[string]interface{}{
		"commands": commands,
	})
}

// Execute runs a single SolidCraft command.
func (c *Client) Execute(command string, params interface{}) (string, error) {
	return c.CallTool("execute", map[string]interface{}{
		"command": command,
		"params":  params,
	})
}

// Measure queries the total volume, area, and bounding box of the active model.
func (c *Client) Measure() (*cadir.VerificationStats, error) {
	out, err := c.CallTool("measure", map[string]interface{}{})
	if err != nil {
		return nil, err
	}

	var result struct {
		Total struct {
			VolumeMM3 float64 `json:"volume_mm3"`
			AreaMM2   float64 `json:"area_mm2"`
		} `json:"total"`
		Bodies []struct {
			BBox struct {
				Min []float64 `json:"min"`
				Max []float64 `json:"max"`
			} `json:"bbox"`
			CenterOfMass []float64 `json:"center_of_mass"`
		} `json:"bodies"`
	}

	if err := json.Unmarshal([]byte(out), &result); err != nil {
		return nil, fmt.Errorf("failed to parse measure output: %w", err)
	}

	stats := &cadir.VerificationStats{
		VolumeMM3: result.Total.VolumeMM3,
		AreaMM2:   result.Total.AreaMM2,
	}

	if len(result.Bodies) > 0 {
		minX, minY, minZ := 1e9, 1e9, 1e9
		maxX, maxY, maxZ := -1e9, -1e9, -1e9
		for _, b := range result.Bodies {
			if len(b.BBox.Min) == 3 && len(b.BBox.Max) == 3 {
				if b.BBox.Min[0] < minX {
					minX = b.BBox.Min[0]
				}
				if b.BBox.Min[1] < minY {
					minY = b.BBox.Min[1]
				}
				if b.BBox.Min[2] < minZ {
					minZ = b.BBox.Min[2]
				}
				if b.BBox.Max[0] > maxX {
					maxX = b.BBox.Max[0]
				}
				if b.BBox.Max[1] > maxY {
					maxY = b.BBox.Max[1]
				}
				if b.BBox.Max[2] > maxZ {
					maxZ = b.BBox.Max[2]
				}
			}
		}
		stats.BBoxMin = cadir.Point3D{X: minX, Y: minY, Z: minZ}
		stats.BBoxMax = cadir.Point3D{X: maxX, Y: maxY, Z: maxZ}
	}

	return stats, nil
}

// Save writes the design to a file.
func (c *Client) Save(path string) error {
	_, err := c.CallTool("save", map[string]interface{}{"path": path})
	return err
}
