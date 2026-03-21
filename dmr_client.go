package main

import (
	"fmt"
	"net/rpc"
	"strings"

	"github.com/seanly/dmr/pkg/plugin/proto"
)

func (p *FeishuPlugin) hostRPC() (*rpc.Client, error) {
	p.hostMu.Lock()
	c := p.hostClient
	p.hostMu.Unlock()
	if c == nil {
		return nil, fmt.Errorf("host RPC client not ready")
	}
	return c, nil
}

func (p *FeishuPlugin) callRunAgent(tapeName, prompt string, historyAfter int32) (*proto.RunAgentResponse, error) {
	c, err := p.hostRPC()
	if err != nil {
		return nil, err
	}
	req := &proto.RunAgentRequest{
		TapeName:            tapeName,
		Prompt:              prompt,
		HistoryAfterEntryID: historyAfter,
	}
	var resp proto.RunAgentResponse
	if err := c.Call("Plugin.RunAgent", req, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// callRestartHost asks the DMR host to run the same restart path as `dmr serve service restart`.
func (p *FeishuPlugin) callRestartHost() (hostErr string, rpcErr error) {
	c, err := p.hostRPC()
	if err != nil {
		return "", err
	}
	var resp proto.RestartHostResponse
	if err := c.Call("Plugin.RestartHost", &proto.RestartHostRequest{}, &resp); err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Error), nil
}
