// dmr-plugin-feishu is an external DMR plugin: Feishu IM -> agent -> Feishu reply,
// plus text-based approvals for private chats.
package main

import (
	goplugin "github.com/hashicorp/go-plugin"
	"github.com/seanly/dmr-plugin-feishu/internal/plugin"
	"github.com/seanly/dmr/pkg/plugin/proto"
)

func main() {
	impl := plugin.New()

	goplugin.Serve(&goplugin.ServeConfig{
		HandshakeConfig: proto.Handshake,
		Plugins: map[string]goplugin.Plugin{
			"dmr-plugin": &proto.DMRPlugin{Impl: impl},
		},
	})
}
