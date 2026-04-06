package dmr

import (
	"encoding/json"
	"net/rpc"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

// Client wraps the RPC client for DMR host communication.
type Client struct {
	client *rpc.Client
}

// NewClient creates a new DMR client wrapper.
func NewClient(client *rpc.Client) *Client {
	return &Client{client: client}
}

// SetClient updates the underlying RPC client.
func (c *Client) SetClient(client *rpc.Client) {
	c.client = client
}

// RunAgent calls the host RunAgent method.
func (c *Client) RunAgent(tape, prompt string, historyAfter int64) (*proto.RunAgentResponse, error) {
	return c.RunAgentWithContext(tape, prompt, historyAfter, nil)
}

// RunAgentWithContext calls the host RunAgent method with plugin context.
// The context is passed to tool calls, allowing tools to access trigger-time information
// without maintaining in-memory state.
func (c *Client) RunAgentWithContext(tape, prompt string, historyAfter int64, ctx map[string]any) (*proto.RunAgentResponse, error) {
	if c.client == nil {
		return nil, nil
	}
	req := &proto.RunAgentRequest{
		TapeName:            tape,
		Prompt:              prompt,
		HistoryAfterEntryID: int32(historyAfter),
	}
	// Encode context if provided
	if ctx != nil && len(ctx) > 0 {
		ctxJSON, _ := json.Marshal(ctx)
		req.ContextJSON = string(ctxJSON)
	}
	var resp proto.RunAgentResponse
	err := c.client.Call("Plugin.RunAgent", req, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
