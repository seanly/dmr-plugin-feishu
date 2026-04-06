# AGENTS.md

> dmr-plugin-feishu — DMR external plugin for Feishu/Lark IM integration.
> "Connect your DMR agent to Feishu private chats."

## Quick Start

```bash
go build -o dmr-plugin-feishu ./cmd/dmr-plugin-feishu/
./dmr-plugin-feishu
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  cmd/dmr-plugin-feishu/      Plugin entry point             │
├─────────────────────────────────────────────────────────────┤
│  internal/plugin/            Plugin lifecycle & RPC handlers │
│    - Init, Shutdown          DMR plugin interface           │
│    - RequestApproval         Single/batch approval          │
│    - ProvideTools, CallTool  Tool registration & execution  │
├─────────────────────────────────────────────────────────────┤
│  internal/bot/               Feishu Bot capabilities        │
│    - client.go               Lark SDK wrapper               │
│    - message.go              Text/Post/Markdown sending     │
│    - file.go                 File upload/download           │
│    - approver.go             Interactive approval UI        │
├─────────────────────────────────────────────────────────────┤
│  internal/inbound/           Incoming message pipeline      │
│    - receiver.go             WebSocket event handler        │
│    - parser.go               Message parsing (text/image/   │
│                              file/post)                     │
│    - context.go              Reply context (quoted parent)  │
│    - dedup.go                Message deduplication          │
│    - filter.go               Sender allowlist               │
├─────────────────────────────────────────────────────────────┤
│  internal/queue/             Per-chat serial processing     │
│    - manager.go              Queue workers per chat_id      │
├─────────────────────────────────────────────────────────────┤
│  internal/tools/             DMR Tools                      │
│    - send_text.go            feishuSendText                 │
│    - send_file.go            feishuSendFile                 │
├─────────────────────────────────────────────────────────────┤
│  internal/dmr/               DMR host communication         │
│    - client.go               RPC client wrapper             │
├─────────────────────────────────────────────────────────────┤
│  internal/prompt/            Prompt engineering             │
│    - extra.go                Built-in hints + user extras   │
├─────────────────────────────────────────────────────────────┤
│  pkg/utils/                  Shared utilities               │
│    - strings.go              Truncate, sanitize             │
│    - path.go                 Path resolution, containment   │
└─────────────────────────────────────────────────────────────┘
```

Dependency direction: `cmd/ → internal/plugin → internal/{bot,inbound,queue,tools,dmr,prompt} → pkg/utils`

## Package Map

| Package | Responsibility | Key Types | Source |
|---------|---------------|-----------|--------|
| `internal/plugin` | Plugin lifecycle, DMR RPC interface | `Plugin`, `Config` | `plugin.go`, `config.go` |
| `internal/bot` | Feishu API client & operations | `Client`, `Instance`, `Approver` | `client.go`, `message.go`, `file.go`, `approver.go` |
| `internal/inbound` | Message receiving & parsing | `Receiver`, `Deduper` | `receiver.go`, `parser.go`, `context.go` |
| `internal/queue` | Job queue management | `Manager`, `Job` | `manager.go` |
| `internal/tools` | DMR tool implementations | `FileClient`, `MessageClient` interfaces | `send_text.go`, `send_file.go` |
| `internal/dmr` | DMR host RPC client | `Client` | `client.go` |
| `internal/prompt` | Prompt composition | `Composer` | `extra.go` |
| `pkg/utils` | Utility functions | `TruncateRunes`, `ResolveSendFilePath` | `strings.go`, `path.go` |

## Data Flow

```
Feishu WebSocket Event
    │
    ▼
internal/inbound/receiver.go
    │
    ├──► internal/inbound/dedup.go ──► Skip if duplicate
    │
    ├──► internal/inbound/parser.go ──► Extract text/content
    │
    ├──► internal/inbound/filter.go ──► Check allowlist
    │
    ├──► internal/inbound/context.go ──► Fetch parent message (if reply)
    │
    ▼
internal/queue/manager.go ──► Enqueue job per chat_id
    │
    ▼
internal/plugin/plugin.go ──► ProcessJob
    │
    ├──► Build context (chat_id, message_id, in_thread, etc.)
    │
    ├──► internal/dmr/client.go ──► Call DMR RunAgentWithContext
    │       │
    │       └──► DMR Host ──► LLM ──► Tool calls
    │               │
    │               └──► internal/plugin/CallTool (feishuSendFile/Text)
    │                       │
    │                       └──► Extract context from CallToolRequest.ContextJSON
    │
    └──► ReplyAgentOutputWithContext ──► internal/bot/message.go
                │
                ▼
            Feishu IM
```

### Context Passing (No Active Job State)

Unlike earlier versions that maintained an "active job" map with timeout-based cleanup, this plugin now uses **context passing**:

1. When `ProcessJob` runs, it builds a context map with `chat_id`, `trigger_message_id`, `in_thread`, etc.
2. This context is passed to `RunAgentWithContext`, which forwards it to the DMR host
3. When tools (`feishuSendFile`, `feishuSendText`) are called, they receive this context via `CallToolRequest.ContextJSON`
4. Tools extract `chat_id` from the context and use `GetBotForChat()` to get the bot instance dynamically

**Benefits:**
- No timeout issues (tools work regardless of how long the agent loop takes)
- Multi-instance safe (context travels with RPC calls)
- Simpler code (no SetActiveJob/ClearActiveJob/GetActiveJobByTape logic)
- Stateless design

## Approval Flow

```
DMR Host requests approval
    │
    ▼
internal/plugin/RequestApproval (or RequestBatchApproval)
    │
    ├──► Get bot for chat_id
    │
    ├──► internal/bot/approver.go ──► Build approval prompt
    │
    ├──► Send approval message to Feishu chat
    │
    └──► Wait for user reply (with timeout)
            │
            ├──► User replies "y/s/a/n"
            │
            └──► internal/inbound/receiver.go ──► TryResolveP2P
                    │
                    └──► Signal approval result
```

## Multi-Bot Support

```go
// internal/plugin/plugin.go
type Plugin struct {
    bots   []*bot.Instance          // Multiple bot instances
    routing map[string]*bot.Instance // chat_id -> bot lookup
}
```

Each bot has:
- Independent Lark client
- Independent WebSocket connection
- Independent approval handler
- Shared routing table for lookups

## Configuration

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-feishu"   # required for external plugin
[plugins.config]
# Multi-bot configuration
[[plugins.config.bots]]
app_id = "cli_xxx"
app_secret = "xxx"
verification_token = "xxx"
encrypt_key = "xxx"
allow_from = ["user_open_id_1", "user_open_id_2"]

# File sending limits
send_file_max_bytes = "30MB"
send_file_root = "/workspace/reports"

# Inbound media download
inbound_media_enabled = true
inbound_media_max_bytes = "30MB"
inbound_media_retention_days = 7

# Reply context
inbound_reply_context_enabled = true

# Extra prompts
extra_prompt = "Custom instructions..."
extra_prompt_file = "prompts/feishu.md"
```

## Conventions

### Adding a New Tool

1. Create `internal/tools/<name>.go`
2. Define params schema function
3. Define execution function
4. Register in `internal/plugin/plugin.go:ProvideTools`
5. Handle in `internal/plugin/plugin.go:CallTool`

### Adding a New Message Handler

1. Extend `internal/inbound/parser.go:ParseFeishuInboundMessage`
2. Add handler in `internal/bot/message.go` or `internal/bot/file.go`
3. Update `internal/inbound/receiver.go` if needed

### Adding a New Bot Capability

1. Add method to `internal/bot/client.go`
2. Update `internal/bot/Instance` if state needed
3. Expose via tool or internal usage

## Tool Naming

| Tool | Description | Parameters |
|------|-------------|------------|
| `feishuSendText` | Send text/Markdown to p2p chat | `text`, `markdown`, `tape_name`/`chat_id` |
| `feishuSendFile` | Upload and send file to p2p chat | `path`, `caption`, `filename` |

## Key Constants

```go
const (
    maxFeishuTextRunes            = 18000
    maxFeishuApprovalMarkdownRunes = 14000
    maxSendFileNameRunes          = 200
    defaultSendFileMaxBytes       = 30 * 1024 * 1024 // 30 MiB
)
```

## Testing

```bash
# Build
go build -o dmr-plugin-feishu ./cmd/dmr-plugin-feishu/

# Run tests
go test ./...

# Run specific package tests
go test ./internal/inbound/...
go test ./internal/tools/...
```

## Debugging

Enable verbose logging:

```go
// Log prefix "feishu:" used throughout
log.Printf("feishu: message received chatType=%q chatID=%q", ...)
log.Printf("feishu: RunAgent empty output tape=%q", ...)
log.Printf("feishu: queue worker started for chat_id=%q", ...)
```

## References

| Topic | Document |
|-------|----------|
| Codebase refactoring | `docs/issues/codebase-refactoring.md` |
| Multi-instance support | `docs/issues/multi-instance-support.md` |
| Group chat design | `docs/issues/group-chat-design.md` |
| DMR Plugin System | `dmr/AGENTS.md` |
| DMR Plugin Proto | `dmr/pkg/plugin/proto/` |
