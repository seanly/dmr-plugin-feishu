package dmr

import (
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
	if c.client == nil {
		return nil, nil
	}
	req := &proto.RunAgentRequest{
		TapeName:            tape,
		Prompt:              prompt,
		HistoryAfterEntryID: int32(historyAfter),
	}
	var resp proto.RunAgentResponse
	err := c.client.Call("Plugin.RunAgent", req, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
