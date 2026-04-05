package bot

import (
	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// Client wraps a Feishu/Lark client with configuration.
type Client struct {
	AppID     string
	AppSecret string
	Lark      *lark.Client
}

// NewClient creates a new Feishu client.
func NewClient(appID, appSecret string) *Client {
	return &Client{
		AppID:     appID,
		AppSecret: appSecret,
		Lark:      lark.NewClient(appID, appSecret),
	}
}

// Instance holds a single Feishu bot's client and runtime state.
type Instance struct {
	Config   ClientConfig
	Client   *Client
	Approver *Approver
	WSClient *larkws.Client // WebSocket client for lifecycle management
}

// ClientConfig holds configuration for bot instance.
type ClientConfig struct {
	AppID             string
	AppSecret         string
	VerificationToken string
	EncryptKey        string
	AllowFrom         []string
}
