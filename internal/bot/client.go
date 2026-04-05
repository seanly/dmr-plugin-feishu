package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

// botInfoResponse represents the response from the bot info API.
type botInfoResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Bot botInfo `json:"bot"`
	} `json:"data"`
}

// botInfo represents the bot info in the API response.
type botInfo struct {
	OpenID    string `json:"open_id"`
	AppID     string `json:"app_id"`
	AppName   string `json:"app_name"`
	BotName   string `json:"bot_name"`
	AvatarURL string `json:"avatar_url"`
}

// GetBotID fetches the bot's open_id from the Feishu API.
func (c *Client) GetBotID(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get tenant access token
	tokenURL := "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal"
	payload := fmt.Sprintf(`{"app_id":"%s","app_secret":"%s"}`, c.AppID, c.AppSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get tenant access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	var tokenResp struct {
		Code              int    `json:"code"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return "", fmt.Errorf("failed to parse token response: %w", err)
	}
	if tokenResp.TenantAccessToken == "" {
		return "", fmt.Errorf("empty tenant access token")
	}

	// Call bot info API
	botReq, err := http.NewRequestWithContext(ctx, "GET", "https://open.feishu.cn/open-apis/bot/v3/info", nil)
	if err != nil {
		return "", fmt.Errorf("failed to create bot info request: %w", err)
	}
	botReq.Header.Set("Authorization", "Bearer "+tokenResp.TenantAccessToken)

	botResp, err := client.Do(botReq)
	if err != nil {
		return "", fmt.Errorf("failed to call bot info API: %w", err)
	}
	defer botResp.Body.Close()

	botBody, err := io.ReadAll(botResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read bot info response: %w", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(botBody, &raw); err != nil {
		return "", fmt.Errorf("failed to parse bot info response: %w, body: %s", err, string(botBody))
	}

	code, _ := raw["code"].(float64)
	msg, _ := raw["msg"].(string)
	if code != 0 {
		return "", fmt.Errorf("bot info API error: code=%d msg=%s, body: %s", int(code), msg, string(botBody))
	}

	bot, ok := raw["bot"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("bot info: missing bot, body: %s", string(botBody))
	}

	openID, ok := bot["open_id"].(string)
	if !ok || openID == "" {
		return "", fmt.Errorf("bot open_id not found in response, body: %s", string(botBody))
	}
	return openID, nil
}

// ChatExists checks if this bot has access to the given chat_id.
func (c *Client) ChatExists(ctx context.Context, chatID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Get tenant access token
	tokenURL := "https://open.feishu.cn/open-apis/auth/v3/tenant_access_token/internal"
	payload := fmt.Sprintf(`{"app_id":"%s","app_secret":"%s"}`, c.AppID, c.AppSecret)

	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to get tenant access token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, fmt.Errorf("failed to read token response: %w", err)
	}

	var tokenResp struct {
		Code              int    `json:"code"`
		TenantAccessToken string `json:"tenant_access_token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return false, fmt.Errorf("failed to parse token response: %w", err)
	}
	if tokenResp.TenantAccessToken == "" {
		return false, fmt.Errorf("empty tenant access token")
	}

	// Call chat info API to check if bot has access to this chat
	chatReq, err := http.NewRequestWithContext(ctx, "GET",
		"https://open.feishu.cn/open-apis/im/v1/chats/"+chatID, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create chat info request: %w", err)
	}
	chatReq.Header.Set("Authorization", "Bearer "+tokenResp.TenantAccessToken)

	chatResp, err := client.Do(chatReq)
	if err != nil {
		return false, fmt.Errorf("failed to call chat info API: %w", err)
	}
	defer chatResp.Body.Close()

	// 200 = exists, 1223 = not found or no access
	if chatResp.StatusCode == 200 {
		return true, nil
	}
	return false, nil
}

// Instance holds a single Feishu bot's client and runtime state.
type Instance struct {
	Config   ClientConfig
	Client   *Client
	Approver *Approver
	WSClient *larkws.Client // WebSocket client for lifecycle management
	BotID    string         // Bot's open_id for mention detection

	// GroupEnabled controls whether this bot receives group messages.
	GroupEnabled bool
	// ApproverOpenID is the open_id of the admin for group chat approvals.
	ApproverOpenID string
}

// ClientConfig holds configuration for bot instance.
type ClientConfig struct {
	AppID             string
	AppSecret         string
	VerificationToken string
	EncryptKey        string
	AllowFrom         []string
}
