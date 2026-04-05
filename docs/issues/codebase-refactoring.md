# dmr-plugin-feishu 代码库重构

> 重构日期：2026-04-05
> 目标：优化代码目录结构，提升可维护性和扩展性

## 背景

重构前，所有代码文件平铺在根目录（flat structure），导致：
- 职责混杂，难以快速定位功能
- 20+ 个文件拥挤在根目录
- 新增功能时不知道放在哪里
- 测试文件与源码混在一起

## 重构后的目录结构

参考 DMR 内置插件的目录风格和 Go 项目最佳实践：

```
dmr-plugin-feishu/
├── cmd/
│   └── dmr-plugin-feishu/
│       └── main.go              # 仅包含 main() 入口
├── internal/
│   ├── plugin/
│   │   ├── plugin.go            # 插件核心：DMR RPC 接口实现
│   │   ├── config.go            # 配置结构体 + 解析逻辑
│   │   └── config_accessor.go   # Config 接口适配（实现 bot.ConfigAccessor）
│   ├── bot/
│   │   ├── client.go            # Feishu/Lark 客户端封装
│   │   ├── message.go           # 消息发送/回复（文本、Markdown、富文本）
│   │   ├── file.go              # 文件上传/下载（资源获取、文件发送）
│   │   └── approver.go          # 审批功能（单条/批量审批）
│   ├── inbound/
│   │   ├── receiver.go          # 消息接收处理器
│   │   ├── parser.go            # 消息解析（文本、图片、文件、富文本）
│   │   ├── context.go           # 回复上下文（引用原消息）
│   │   ├── dedup.go             # 消息去重
│   │   └── filter.go            # 发送者过滤器
│   ├── queue/
│   │   └── manager.go           # 任务队列管理（按 chat_id 串行处理）
│   ├── tools/
│   │   ├── send_text.go         # feishuSendText 工具实现
│   │   └── send_file.go         # feishuSendFile 工具实现
│   ├── dmr/
│   │   └── client.go            # DMR RPC 客户端封装
│   └── prompt/
│       └── extra.go             # 额外提示词（内置提示 + 用户自定义）
├── pkg/
│   └── utils/
│       ├── strings.go           # 字符串工具（截断、处理）
│       └── path.go              # 路径处理（解析、安全检查）
├── docs/
│   ├── architecture.md          # 架构设计文档
│   ├── implementation.md        # 实现细节
│   └── issues/
│       ├── codebase-refactoring.md   # 本文档
│       ├── group-chat-design.md      # 群聊功能设计
│       └── multi-instance-support.md # 多实例支持
└── go.mod, go.sum, Makefile, README.md
```

## 设计原则

### 1. 按职责分层

| 目录 | 职责 | 对应 DMR 内置插件 |
|------|------|------------------|
| `cmd/` | 可执行入口 | `cmd/dmr/` |
| `internal/plugin/` | 插件生命周期管理 | `plugins/webtool/plugin.go` |
| `internal/bot/` | Feishu Bot 能力封装 | `plugins/shell/shell_manager.go` |
| `internal/inbound/` | 入站消息处理链 | 接收→解析→过滤→上下文 |
| `internal/queue/` | 任务队列管理 | 独立模块 |
| `internal/tools/` | DMR 工具实现 | `plugins/webtool/fetch.go` |
| `internal/dmr/` | DMR 主机通信 | RPC 客户端 |
| `pkg/utils/` | 通用工具函数 | 可复用的辅助函数 |

### 2. 解耦设计

使用接口避免包间循环依赖：

```go
// internal/tools/send_file.go
type FileClient interface {
    SendTextToChat(ctx context.Context, chatID, text string) error
    SendFileFromReader(ctx context.Context, chatID, triggerMessageID string, inThread bool, fileName string, r io.Reader) (string, error)
}

// internal/bot/file.go
func (c *Client) SendFileFromReader(...) (string, error) { ... }
```

### 3. 配置抽象

```go
// internal/bot/file.go - ConfigAccessor 接口
type ConfigAccessor interface {
    InboundMediaEnabled() bool
    InboundMediaMaxBytes() int64
    InboundMediaTimeout() time.Duration
    InboundStorageRoot() (string, error)
    InboundMediaRetentionDays() int
}
```

## 文件映射（重构前后对比）

| 原文件 | 新位置 | 说明 |
|--------|--------|------|
| `main.go` | `cmd/dmr-plugin-feishu/main.go` | 入口文件 |
| `plugin.go` | `internal/plugin/plugin.go` | RPC 方法实现 |
| `config.go` | `internal/plugin/config.go` | 配置结构体 |
| `receiver.go` | `internal/inbound/receiver.go` | 消息接收 |
| `parse.go` | `internal/inbound/parser.go` | 消息解析 |
| `reply.go` | `internal/bot/message.go` | 消息发送 |
| `file_send.go` | `internal/bot/file.go` | 文件操作（合并） |
| `receive_media.go` | `internal/bot/file.go` | 媒体接收（合并） |
| `approver.go` | `internal/bot/approver.go` | 审批功能 |
| `send_text_tool.go` | `internal/tools/send_text.go` | 工具实现 |
| `send_file_tool.go` | `internal/tools/send_file.go` | 工具实现 |
| `queue.go` | `internal/queue/manager.go` | 队列管理 |
| `dmr_client.go` | `internal/dmr/client.go` | RPC 客户端 |
| `dedup.go` | `internal/inbound/dedup.go` | 去重 |
| `filter.go` | `internal/inbound/filter.go` | 过滤 |
| `reply_context.go` | `internal/inbound/context.go` | 回复上下文 |
| `extra_prompt.go` | `internal/prompt/extra.go` | 提示词 |

## 扩展指南

### 新增 Feishu API 功能

在 `internal/bot/` 添加新文件：

```go
// internal/bot/calendar.go
package bot

func (c *Client) CreateCalendarEvent(ctx context.Context, ...)
func (c *Client) GetCalendarEvent(ctx context.Context, ...)
```

### 新增 DMR 工具

在 `internal/tools/` 添加：

```go
// internal/tools/create_task.go
package tools

func CreateTaskParams() string { ... }
func ExecuteCreateTask(...) (map[string]any, error) { ... }
```

然后在 `internal/plugin/plugin.go` 注册：

```go
func (p *Plugin) ProvideTools(...) {
    resp.Tools = []proto.ToolDef{
        { Name: "feishuCreateTask", ... },
    }
}
```

### 新增入站处理中间件

在 `internal/inbound/` 添加：

```go
// internal/inbound/middleware_auth.go
package inbound

func (r *Receiver) AuthMiddleware(ctx context.Context, ...) error { ... }
```

## 测试建议

每个包可以独立测试：

```bash
# 测试解析器
go test ./internal/inbound/...

# 测试工具
go test ./internal/tools/...

# 测试完整插件
go test ./...
```

## 注意事项

1. **避免循环依赖**：使用接口解耦，如 `tools.FileClient` 接口由 `bot.Client` 实现
2. **配置访问**：通过 `ConfigAccessor` 接口访问配置，避免直接依赖 `plugin.Config`
3. **包可见性**：所有内部实现使用 `internal/` 路径，防止外部包导入
4. **工具函数**：通用工具放在 `pkg/utils/`，可被其他插件引用

## 参考

- DMR 插件架构：`dmr/AGENTS.md`
- DMR 内置插件：`dmr/plugins/webtool/`, `dmr/plugins/shell/`
- Go 项目标准布局：https://github.com/golang-standards/project-layout
