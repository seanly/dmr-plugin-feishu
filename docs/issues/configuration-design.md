# Feishu Plugin Configuration Design

> 飞书插件配置系统设计文档
>
> 涵盖：配置结构、设计决策、验证规则、向后兼容

## 目录

- [1. 配置概览](#1-配置概览)
- [2. TOML 结构详解](#2-toml-结构详解)
- [3. 设计决策](#3-设计决策)
- [4. 配置验证](#4-配置验证)
- [5. 向后兼容](#5-向后兼容)
- [6. 安全配置](#6-安全配置)

---

## 1. 配置概览

### 1.1 配置层级

```
DMR Config (TOML/YAML)
    └── plugins[]
            └── feishu plugin config
                    ├── bots[]          # 多 Bot 配置
                    ├── admins[]        # 群聊管理员
                    ├── file settings   # 文件上传
                    ├── media settings  # 入站媒体
                    └── misc settings   # 其他配置
```

### 1.2 配置加载流程

```
DMR 启动
    ↓
Load plugins
    ↓
Parse plugin config (JSON)
    ↓
Convert to Config struct
    ↓
Validate
    ↓
Backward compat conversion
    ↓
Apply defaults
    ↓
Plugin.Init()
```

---

## 2. TOML 结构详解

### 2.1 最小配置（单 Bot，仅私聊）

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "./dmr-plugin-feishu"

[plugins.config]
app_id = "cli_xxxxxxxxxxxxxxxx"
app_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
verification_token = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
encrypt_key = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
```

### 2.2 完整配置（多 Bot + 群聊）

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "./dmr-plugin-feishu"

[plugins.config]
# ========== 群聊配置（必须在 bots[] 之前）==========
group_enabled = true

# ========== 文件上传配置 ==========
send_file_max_bytes = "30MB"
send_file_root = "/home/user/workspace"

# ========== 入站媒体配置 ==========
inbound_media_enabled = true
inbound_media_max_bytes = "10MB"
inbound_media_dir = "feishu-inbound"
inbound_media_timeout_sec = 45
inbound_media_retention_days = 7

# ========== 回复上下文配置 ==========
inbound_reply_context_enabled = true
inbound_reply_context_timeout_sec = 12
inbound_reply_context_max_runes = 8000

# ========== 审批配置 ==========
approval_timeout_sec = 300
dedup_ttl_minutes = 10

# ========== 额外 Prompt ==========
extra_prompt = "You are a helpful assistant."
extra_prompt_file = "./feishu-prompt.txt"

# ========== 多 Bot 配置（[plugins.config] 普通字段之后）==========
[[plugins.config.bots]]
app_id = "cli_xxxxxxxxxxxxxxxx"
app_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
verification_token = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
encrypt_key = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
allow_from = ["ou_xxxxxxxxxxxxxxxx", "ou_yyyyyyyyyyyyyyyy"]
bot_open_id = "ou_bot_xxxxxxxxxxxxxxxx"

[[plugins.config.bots]]
app_id = "cli_yyyyyyyyyyyyyyyy"
app_secret = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
verification_token = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
encrypt_key = "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"
allow_from = []
bot_open_id = "ou_bot_yyyyyyyyyyyyyyyy"

# ========== 群聊管理员配置（放在最后）==========
[[plugins.config.admins]]
open_id = "ou_admin_xxxxxxxxxxxxxxxx"
name = "管理员1"

[[plugins.config.admins]]
open_id = "ou_admin_yyyyyyyyyyyyyyyy"
name = "管理员2"
```

> **⚠️ 重要**：TOML 中数组元素 `[[...]]` 之后的简单字段会被解析为数组元素的属性。因此 `[plugins.config]` 的普通字段（如 `group_enabled`）必须写在 `[[plugins.config.bots]]` **之前**！

### 2.3 配置项详细说明

#### 2.3.1 Bot 配置 (`bots`)

```toml
[[plugins.config.bots]]
app_id = "cli_xxxxxxxxxxxxxxxx"                          # 飞书应用 ID
app_secret = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"          # 飞书应用密钥
verification_token = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"  # 事件订阅验证 Token
encrypt_key = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"         # 消息加密密钥
allow_from = ["ou_xxxxxxxxxxxxxxxx"]                     # 允许的用户 OpenID（可选）
bot_open_id = "ou_bot_xxxxxxxxxxxxxxxx"                  # Bot OpenID（群聊需要）
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `app_id` | string | ✅ | - | 飞书开放平台应用 ID |
| `app_secret` | string | ✅ | - | 飞书开放平台应用密钥 |
| `verification_token` | string | ✅ | - | 事件订阅验证令牌 |
| `encrypt_key` | string | ✅ | - | 消息加密密钥（空字符串表示不加密）|
| `allow_from` | []string | ❌ | [] | 允许访问的用户 OpenID 列表，空列表允许所有 |
| `bot_open_id` | string | ❌ | "" | Bot 的 Open ID，用于群聊 @mention 检测 |

#### 2.3.2 群聊配置

```toml
[plugins.config]
group_enabled = true  # 启用群聊功能

[[plugins.config.admins]]
open_id = "ou_admin_xxxxxxxxxxxxxxxx"  # 管理员 OpenID
name = "管理员1"                        # 显示名称（日志用）
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `group_enabled` | bool | ❌ | false | 是否启用群聊功能 |
| `admins` | []object | 条件 | [] | 群聊管理员列表，`group_enabled=true` 时建议配置 |

**Admin 对象**：

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `open_id` | string | ✅ | 管理员的用户 OpenID |
| `name` | string | ❌ | 显示名称，用于日志记录 |

#### 2.3.3 文件上传配置

```toml
[plugins.config]
send_file_max_bytes = "30MB"           # 最大文件大小
send_file_root = "/home/user/workspace" # 文件路径限制（可选）
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `send_file_max_bytes` | string/int | ❌ | "30MB" | 最大文件大小，支持单位：B, KB, MB, GB |
| `send_file_root` | string | ❌ | "" | 文件路径限制，设置后只能发送此目录下的文件 |

#### 2.3.4 入站媒体配置

```toml
[plugins.config]
inbound_media_enabled = true           # 启用媒体下载
inbound_media_max_bytes = "10MB"       # 最大下载大小
inbound_media_dir = "feishu-inbound"   # 保存目录
inbound_media_timeout_sec = 45         # 下载超时
inbound_media_retention_days = 7       # 保留天数
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `inbound_media_enabled` | bool | ❌ | true | 是否下载入站媒体（图片、文件）|
| `inbound_media_max_bytes` | string/int | ❌ | 30MB | 单个媒体最大大小 |
| `inbound_media_dir` | string | ❌ | "feishu-inbound" | 媒体保存目录（相对于 workspace）|
| `inbound_media_timeout_sec` | int | ❌ | 45 | 媒体下载超时（秒）|
| `inbound_media_retention_days` | int | ❌ | 0 | 媒体文件保留天数，0 表示不自动清理 |

#### 2.3.5 回复上下文配置

```toml
[plugins.config]
inbound_reply_context_enabled = true      # 启用回复上下文
inbound_reply_context_timeout_sec = 12    # 获取父消息超时
inbound_reply_context_max_runes = 8000    # 父消息最大字符数
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `inbound_reply_context_enabled` | bool | ❌ | true | 是否获取父消息内容作为上下文 |
| `inbound_reply_context_timeout_sec` | int | ❌ | 12 | 获取父消息的超时时间（秒）|
| `inbound_reply_context_max_runes` | int | ❌ | 8000 | 父消息内容最大字符数 |

#### 2.3.6 其他配置

```toml
[plugins.config]
approval_timeout_sec = 300    # 审批超时（秒）
dedup_ttl_minutes = 10        # 去重 TTL（分钟）
extra_prompt = "..."          # 额外 Prompt
extra_prompt_file = "..."     # 额外 Prompt 文件路径
```

| 字段 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `approval_timeout_sec` | int | ❌ | 300 | 审批等待超时时间（秒）|
| `dedup_ttl_minutes` | int | ❌ | 10 | 消息去重时间窗口（分钟）|
| `extra_prompt` | string | ❌ | "" | 附加到系统 Prompt 的内容 |
| `extra_prompt_file` | string | ❌ | "" | 附加 Prompt 文件路径（UTF-8 文本）|

---

## 3. 设计决策

### 3.1 为什么使用 TOML？

**决策**：支持 TOML 作为配置文件格式。

**原因**：
- TOML 语法简洁，易于手写
- 支持注释，便于维护
- 层次结构清晰
- DMR 主程序已支持 TOML

**示例对比**：

```yaml
# YAML
plugins:
  - name: feishu
    config:
      bots:
        - app_id: "cli_xxx"
```

```toml
# TOML
[[plugins]]
name = "feishu"

[[plugins.config.bots]]
app_id = "cli_xxx"
```

### 3.2 多 Bot 设计

**决策**：支持配置多个 Bot 实例。

**原因**：
- 不同业务可能需要不同 Bot
- 多租户场景
- 高可用（一个 Bot 失效，其他可用）

**路由策略**：
```
消息到达
    ↓
提取 chat_id
    ↓
routing[chat_id] → Bot Instance
    ↓
使用对应 Bot 回复
```

### 3.3 大小单位设计

**决策**：支持带单位的大小字符串（如 "30MB"）。

**支持的单位**：
- B（字节）
- KB / K（千字节）
- MB / M（兆字节）
- GB / G（吉字节）

**解析逻辑**：
```go
func ParseSize(s string) (int64, error) {
    // "30MB" → 30 * 1024 * 1024
    // "1.5GB" → 1.5 * 1024 * 1024 * 1024
}
```

### 3.4 TOML 数组配置顺序

**问题**：TOML 中数组元素 `[[...]]` 会影响后续字段的解析范围。

**示例**：
```toml
# ❌ 错误：group_enabled 会被解析为 bots 数组元素的属性
[[plugins.config.bots]]
app_id = "cli_xxx"

group_enabled = true  # 这属于 bots[0]！

# ✅ 正确：普通字段在数组元素之前
[plugins.config]
group_enabled = true  # 这属于 plugins.config

[[plugins.config.bots]]
app_id = "cli_xxx"
```

**规则**：
1. `[table]` 普通字段 **先于** `[[array]]` 数组元素
2. 多个数组按依赖顺序排列（`bots` → `admins`）

### 3.5 Allowlist 设计

**决策**：`allow_from` 为空列表时允许所有用户。

**原因**：
- 便于快速启动测试
- 生产环境建议配置限制

**安全检查**：
```go
func IsAllowedSender(allowList []string, senderID string) bool {
    if len(allowList) == 0 {
        return true  // 空列表允许所有
    }
    // 检查 senderID 是否在 allowList 中
}
```

---

## 4. 配置验证

### 4.1 验证规则

| 规则 | 级别 | 说明 |
|------|------|------|
| `app_id` 非空 | Error | Bot 必需 |
| `app_secret` 非空 | Error | Bot 必需 |
| `send_file_max_bytes` > 0 | Warning | 无效值使用默认值 |
| `inbound_media_dir` 不包含 `..` | Error | 防止目录遍历 |
| `group_enabled=true` 时建议配置 `admins` | Warning | 群聊审批需要管理员 |

### 4.2 验证流程

```
ParseConfig
    ↓
Required fields check
    ↓
Size values parse
    ↓
Path validation
    ↓
Apply defaults
    ↓
Return Config
```

### 4.3 路径安全

**入站媒体目录验证**：
```go
func (c Config) GetInboundStorageRoot() (string, error) {
    // 1. 清理路径
    sub = filepath.Clean(sub)
    
    // 2. 检查是否包含 ..
    if strings.Contains(sub, "..") {
        return "", fmt.Errorf("invalid inbound_media_dir")
    }
    
    // 3. 检查是否逃逸出 workspace
    if rel, err := filepath.Rel(base, joined); 
       strings.HasPrefix(rel, "..") {
        return "", fmt.Errorf("inbound_media_dir escapes workspace")
    }
}
```

---

## 5. 向后兼容

### 5.1 单 Bot 配置迁移

**旧配置**（仍支持）：
```toml
[plugins.config]
app_id = "cli_xxx"
app_secret = "xxx"
```

**转换逻辑**：
```go
if len(cfg.Bots) == 0 && cfg.AppID != "" {
    cfg.Bots = []BotConfig{{
        AppID:     cfg.AppID,
        AppSecret: cfg.AppSecret,
        // ...
    }}
}
```

### 5.2 配置版本演进

| 版本 | 变更 | 兼容处理 |
|------|------|---------|
| v1.0 | 单 Bot | - |
| v1.1 | 多 Bot | 自动转换旧配置 |
| v1.2 | 群聊支持 | 新增字段，默认关闭 |
| v1.3 | 入站媒体 | 新增字段，默认启用 |

### 5.3 废弃字段处理

| 字段 | 状态 | 替代方案 |
|------|------|---------|
| `app_id` (root) | 已废弃 | 使用 `bots[].app_id` |
| `app_secret` (root) | 已废弃 | 使用 `bots[].app_secret` |

---

## 6. 安全配置

### 6.1 凭证管理

**环境变量支持**：
```toml
[plugins.config]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
```

**DMR 会在加载时自动展开环境变量。**

### 6.2 最小权限原则

**建议配置**：
```toml
[plugins.config]
# 只允许特定用户
allow_from = ["ou_xxx", "ou_yyy"]

# 限制文件路径
send_file_root = "/safe/workspace"

# 限制文件大小
send_file_max_bytes = "10MB"

# 媒体自动清理
inbound_media_retention_days = 1
```

### 6.3 审计日志

**关键操作记录**：
```
feishu: processJob tape=...          # 消息处理
feishu: CallTool feishuSendText      # 工具调用
feishu: approval request for ...     # 审批请求
feishu: file uploaded ...            # 文件上传
```

---

## 附录：配置模板

### 模板 1：开发测试

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "./dmr-plugin-feishu"

[plugins.config]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
verification_token = "${FEISHU_VERIFICATION_TOKEN}"
encrypt_key = "${FEISHU_ENCRYPT_KEY}"
# 开发环境允许所有
allow_from = []
```

### 模板 2：生产私聊

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "/usr/local/bin/dmr-plugin-feishu"

[plugins.config]
# 基础配置
send_file_max_bytes = "30MB"
inbound_media_retention_days = 7

# Bot 配置
[[plugins.config.bots]]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
verification_token = "${FEISHU_VERIFICATION_TOKEN}"
encrypt_key = "${FEISHU_ENCRYPT_KEY}"
allow_from = ["ou_admin_xxx", "ou_dev1_yyy"]
```

### 模板 3：生产群聊

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "/usr/local/bin/dmr-plugin-feishu"

[plugins.config]
# 基础配置（必须在 bots[] 之前）
group_enabled = true
send_file_max_bytes = "30MB"
send_file_root = "/data/workspace"
inbound_media_retention_days = 3

# Bot 配置
[[plugins.config.bots]]
app_id = "${FEISHU_APP_ID}"
app_secret = "${FEISHU_APP_SECRET}"
verification_token = "${FEISHU_VERIFICATION_TOKEN}"
encrypt_key = "${FEISHU_ENCRYPT_KEY}"
bot_open_id = "${FEISHU_BOT_OPEN_ID}"
allow_from = []

# 管理员配置（放在最后）
[[plugins.config.admins]]
open_id = "ou_admin_xxx"
name = "Admin"
```

---

*文档版本: 1.0*
*最后更新: 2026-04-05*
