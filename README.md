# dmr-plugin-feishu

External [DMR](https://github.com/seanly/dmr) plugin (HashiCorp **go-plugin** + `net/rpc`) that connects **Feishu/Lark private (p2p) chat** to the DMR agent: inbound messages trigger `RunAgent`, replies go back to the **thread** or **chat**, and `require_approval` is routed using `ApprovalRequest.Tape`. **Group chats are not handled** (messages are ignored).

Chinese design notes: [`docs/implementation.md`](docs/implementation.md), [`docs/architecture.md`](docs/architecture.md).

## Requirements

- Go **1.25+** (matches DMR)
- A checked-out **dmr** repo next to this one (see `go.mod` `replace`) or adjust the replace path
- Feishu app with **WebSocket** mode / `im.message.receive_v1`, plus `app_id`, `app_secret`, `verification_token`, and (if used) `encrypt_key`
- For **`feishuSendFile`**: enable the app permissions needed to **upload IM files** and **send file messages** (see Feishu open platform: IM message / file APIs for your app type)
- For **`feishuSendText`**: IM **create/send message** permissions for the bot (same family as normal agent replies to p2p chats)
- For **reply / quote context** (default on): Feishu app permission to **get** IM messages (`im/v1/messages/:message_id`), in addition to receiving events
- For **inbound image/file download** (default on): Feishu [**message resource** get](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-resource/get) permissions; set `inbound_media_enabled: false` if you do not use downloads

## Build

```bash
make build    # go mod tidy + go build -o dmr-plugin-feishu .
make test     # go test ./...
make install  # copies binary to ~/.dmr/plugins/
```

Cross-compile: `make cross-build`.

## DMR configuration

1. Build or install the plugin binary.
2. Register it as an **external** plugin with `path` pointing to the executable.
3. Use this plugin as the **approver** (it implements `ProvideApprover` via RPC). Disable **`cli_approver`** (or another approver) so only one approver is active.

Example snippet (`~/.dmr/config.toml`):

```toml
[[plugins]]
name = "opa_policy"
enabled = true

[[plugins]]
name = "cli_approver"
enabled = false

[[plugins]]
name = "feishu"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-feishu"   # or absolute path to the binary
[plugins.config]
app_id = "cli_xxx"
app_secret = "xxx"
verification_token = "xxx"
encrypt_key = ""                         # optional, if encryption enabled on the app
allow_from = []                          # empty = all senders; else allowlist of user ids
approval_timeout_sec = 300
dedup_ttl_minutes = 10
# optional — feishuSendFile
# send_file_max_bytes = 31457280         # default 30 MiB
# send_file_root = "/safe/read-only/dir" # if set, path args must stay under this dir
# optional — inbound image/file (p2p): download to DMR workspace (requires Feishu message-resource scopes; default on)
# inbound_media_enabled = false
# inbound_media_max_bytes = 31457280
# inbound_media_dir = "feishu-inbound"
# inbound_media_timeout_sec = 45
# inbound_media_retention_days = 7       # 0 = no auto cleanup of date subfolders
# optional — quote/reply parent message context for RunAgent (im/v1 message/get)
# inbound_reply_context_enabled = true   # default true; set false to skip
# inbound_reply_context_timeout_sec = 12
# inbound_reply_context_max_runes = 8000
# optional — Feishu-only instructions prepended to inbound RunAgent user message (see below)
# extra_prompt = """
# When using cron reminders on this tape, always call feishuSendText with tape_name.
# """
# extra_prompt_file = "prompts/feishu_extra.md"   # relative to ~/.dmr/ (config file directory)
# optional — restart DMR from Feishu (same as `dmr serve service restart` on the host)
# allow_from = ["ou_xxx"]                       # required when using dmr_restart_token
# dmr_restart_trigger = ",dmr-restart"          # default; first line of message must start with this
# dmr_restart_token = "long-random-secret"      # message: ",dmr-restart long-random-secret"
```

Plugin `config` is passed through DMR as JSON; field names match the struct tags below. Legacy keys such as `group_trigger` in TOML are ignored if present in the JSON subset DMR sends.

## Plugin config fields

| Field | Description |
|-------|-------------|
| `app_id` | Feishu app ID |
| `app_secret` | Feishu app secret |
| `verification_token` | Event subscription verification token |
| `encrypt_key` | Optional encrypt key for events |
| `allow_from` | Allowed sender IDs (`user_id` / `open_id` / `union_id` style strings). Empty = allow all. |
| `approval_timeout_sec` | P2P text approval wait timeout (default `300`). |
| `dedup_ttl_minutes` | Dedup window for `message_id` (default `10`). |
| `send_file_max_bytes` | Max bytes for **`feishuSendFile`** uploads (default `31457280` = 30 MiB). |
| `send_file_root` | If non-empty, `path` arguments must resolve under this absolute directory; if empty, paths are restricted to the plugin process **current working directory** at resolve time. |
| `workspace` | Injected by DMR (absolute path, same as `fs` / `shell`). Used as the base directory for **inbound** file/image downloads (`<workspace>/<inbound_media_dir>/…`). If missing, falls back to `config_base_dir`. |
| `inbound_media_enabled` | Default **`true`**. When enabled, user-sent **`image`** and **`file`** messages in p2p are fetched via Feishu [message resource](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message-resource/get) and saved under the workspace; `RunAgent` user text includes a `local_path` line. Set `false` to disable downloads. Requires app scopes for that API when enabled. |
| `inbound_media_max_bytes` | Max size per download (default: same as `send_file_max_bytes`). |
| `inbound_media_dir` | Subdirectory under workspace (default `feishu-inbound`). Must not contain `..`. |
| `inbound_media_timeout_sec` | Per-download HTTP timeout (default `45`). |
| `inbound_media_retention_days` | If greater than `0`, on plugin **Init** (after a short delay) date folders `YYYY-MM-DD` under the inbound dir older than this many **local** days are removed. Set `0` to disable cleanup. |
| `inbound_reply_context_enabled` | Default **`true`**. When `true`, p2p events with **`parent_id`** fetch that message ([**GET** im/v1/messages/:message_id](https://open.feishu.cn/document/uAjLw4CM/ukTMukTMukTM/reference/im-v1/message/get)) and prepend a fixed `<<< feishu_quoted_message` block before the user’s new text for `RunAgent`. Requires app permissions to read the message. If the event has no `parent_id`, behavior is unchanged. |
| `inbound_reply_context_timeout_sec` | Timeout for each parent **`message.get`** (default **`12`**). |
| `inbound_reply_context_max_runes` | Max runes of the **parent** body inside the quoted block after parsing (default **`8000`**); longer bodies get `...(truncated)`. |
| `extra_prompt` | Optional multiline text. Combined with `extra_prompt_file` and **prefixed** to each **Feishu inbound** `RunAgent` prompt (before the real user message, separated by `---`). Not an extra LLM API `system` message — DMR still uses global `agent.system_prompt` for the system role. |
| `extra_prompt_file` | Optional path to a UTF-8 file. **Relative paths** are resolved against DMR’s injected **`config_base_dir`** (the directory containing your main `config.toml`). Loaded at plugin **Init**; content is placed **before** `extra_prompt` when both are set. |
| `dmr_restart_trigger` | Prefix for the admin restart line (default `,dmr-restart`). Only the **first line** of the message is checked. |
| `dmr_restart_token` | If non-empty, enables restart: send a p2p message whose first line is `<dmr_restart_trigger> <token>` to trigger **`dmr serve service restart`** on the DMR host. **Requires non-empty `allow_from`.** High risk — use a long random token. |

### Feishu-only extra prompt (limitations)

- **Built-in scheduling hint**: Every Feishu inbound `RunAgent` automatically prepends a short fixed reminder (Chinese + English) that cron-triggered runs need **`feishuSendText`** with **`tape_name`** to reach IM, and mentions **`run_once`**. This is not controlled by `extra_prompt`; it reduces silent mis-delivery when users ask for reminders.
- **Built-in report delivery hint**: A second fixed prefix (Chinese + English) states that **all report-style** deliverables (analysis, summaries, assessments, scan dumps, etc.) must be written to a file and sent **only** via **`feishuSendFile`**; **`feishuSendText` must not carry report body** (any length). **`feishuSendText`** is for brief non-report messages only (`markdown=true` when useful). Not controlled by `extra_prompt`.
- **Tape audit**: DMR records the **full** string passed to `RunAgent` as one `role=user` entry. The prefix (builtin + optional `extra_prompt`) appears in tape history, not only the raw Feishu text. For a cleaner transcript, rely more on DMR’s global **`system_prompt`**.
- **Cron**: Jobs started by **`dmr-plugin-cron`** call the host directly; they **do not** go through this Feishu inbound prefix. Put instructions in the cron job **`prompt`** or in global **`system_prompt`**.
- **External plugins** cannot register DMR’s `SystemPrompt` hook without host/proto changes; optional `extra_prompt` is an additional plugin-side supplement.

## Reply / quoted message context (p2p)

- Feishu often sends only the **newly typed line** in the event `content` when the user **quotes or replies** to an earlier bubble; the quoted text is not duplicated in `content`, but the event may contain **`parent_id`**.
- With **`inbound_reply_context_enabled`** (default on), the plugin loads the parent message once, parses it like other inbound messages (including **`inbound_media_enabled`** downloads for parent **image** / **file** using the **parent** `message_id`), and prepends a delimited block with `parent_message_id`, optional `current_message_id` / `root_message_id`, then the user line. That is how a **quoted reply** to an earlier **file/image** message still reaches the model with `local_path` context.
- **Human approval** (`y` / `n` / …) and **`dmr_restart`** still match **only the user-typed text**, not the quoted block.
- If the trimmed user message looks like a **comma command** (ASCII `,` or full-width `，` as the first character after trim), the quoted block is **omitted** so DMR’s command intercept (first line `,…`) keeps working.
- If **`parent_id`** is missing in the event, or **`message.get`** fails, the prompt is the user text only (or `[empty message]` when empty).

## Inbound images and files (p2p)

- **Standalone file/image (no `parent_id`)**: The resource is still **downloaded and saved** (when **`inbound_media_enabled`** is on) during inbound processing, but **`RunAgent` is not started** — nothing is sent to the model for that message alone. When the user later **quotes/replies** to that message, the usual reply-context path loads the parent and includes parsed text / `local_path` in the prompt.
- **Non-text messages** (for prompts that do run): `image`, `file`, and `post` are turned into structured user text (keys and, for `post`, best-effort plain text). Raw JSON alone is not passed for those types.
- **Download**: By default (**`inbound_media_enabled`** on), **`image`** and **`file`** resources are downloaded **before** the usual routing decision, using `message_id` + resource key and `type`=`image`|`file` as required by Feishu. Failures set `status: download_failed` and `reason:` in the user message (no fake success).
- **Storage layout**: `<workspace>/<inbound_media_dir>/<YYYY-MM-DD>/<message_id>_<filename>`.
- **Privacy / OPA**: Content is written on the DMR host. Align **`fsRead`** / **`fsWrite`** policies (e.g. allow paths under workspace including `feishu-inbound`). Inbound download is separate from **`feishuSendFile`** approvals.

## Tool: `feishuSendFile`

The plugin registers **`feishuSendFile`** via `ProvideTools` / `CallTool` (same mechanism as other external-plugin tools).

- **When it works**: Only while DMR is running the agent for a **Feishu-triggered** private-chat job (between `RunAgent` start and end). The tool sends to the same destination as normal agent replies (`reply_in_thread` when the user message was in a thread).
- **Arguments**: Required **`path`** (local file). Optional **`filename`** overrides the upload display name; optional **`caption`** sends a plain-text line first.
- **Path safety**: Resolved paths must lie under `send_file_root` (if set) or under the process **CWD**; `..` escapes are rejected.
- **OPA**: Treat like other sensitive outbound actions (e.g. `fsWrite`): add or extend Rego so `feishuSendFile` is **`require_approval`** or **`deny`** by default in your policy bundle; the feishu plugin does not change DMR’s default `default.rego`.

## Tool: `feishuSendText`

Sends a **plain text** or **Markdown (post)** message into a **p2p** chat. Use this when the model must push a message **before** the turn ends, or when **`RunAgent` was not started by Feishu** (e.g. **`dmr-plugin-cron`** on tape `feishu:p2p:<chat_id>`), where the host does not call the plugin’s `replyAgentOutput` for you.

- **Feishu-triggered run** (active inbound job): set **`text`** only (optional **`markdown`**). Do **not** set **`tape_name`** or **`chat_id`** — the current chat (and thread, if applicable) is used, same routing as normal replies.
- **No inbound context** (cron, etc.): set **`text`** and exactly one of **`tape_name`** (`feishu:p2p:<chat_id>`, same as the DMR tape) or **`chat_id`**. Sends a **new** message to that chat (no thread reply; there is no trigger message id).
- **`markdown`**: default `false` (plain `msg_type=text`). If `true`, tries rich **`post`** with Markdown, then falls back to plain text on API failure.
- **Security**: **`chat_id` / `tape_name`** allow messaging arbitrary p2p chats the bot can access. Restrict with **OPA** (e.g. **`require_approval`** or **`deny`** by default, allow only when `input.tool` is safe for your deployment).

## Tape names (and queues)

Each inbound **private** message uses tape **`feishu:p2p:<chat_id>`** for DMR (`RunAgent` / history). **Execution is serialized on one global queue**: one worker processes jobs strictly in order, regardless of chat. Different chats keep different tapes, but two users messaging at once will not run two agents **concurrently** in the plugin.

## Context window

The plugin always calls **`RunAgent`** with **`HistoryAfterEntryID = 0`**. DMR loads tape history using its default **last-anchor** context (`tape.NewLastAnchorContext()`). The plugin does **not** call **`TapeHandoff`**; longer conversations rely on DMR core compaction / auto-handoff if configured there.

## Replies

- **In a thread** (rare in p2p): reply to the trigger message with **`reply_in_thread=true`**.
- **Otherwise**: **`Message.Create`** to the chat.

## Approvals (`require_approval`)

OPA must return `require_approval` as usual. DMR fills **`ApprovalRequest.Tape`** from the tool context tape name.

- Only tape prefix **`feishu:p2p:`** is supported; any other tape is denied without a DM prompt.
- The bot sends an approval prompt in that private chat as a **rich post** (`msg_type=post`) with **Markdown** (`tag: md`), matching how agent replies are formatted. If the post fails (API error, size limits, etc.), it **falls back** to plain text so the approval flow still works. The message is truncated to stay within Feishu limits. **Single** approval waits for a **single-character** reply:
  - `y` — approve once  
  - `s` — approve session  
  - `a` — approve always  
  - `n` — deny  

**Batch** approval (same tape routing for every item) follows the **CLI approver** semantics: shared **Reason** / **Risk**, numbered commands with **`shell`** shown as a command block (not raw JSON). Reply with **`y` / `yes`** (all once), **`s` / `session`**, **`a` / `always`**, **`n` / `no`**, or **1-based indices** such as `1` or `1,3,5` to approve only those lines (once). Invalid index lists are treated as deny-all, like the CLI.

## Development

```bash
go test ./...
```

This module uses:

```go
replace github.com/seanly/dmr => ../dmr
```

Adjust if your DMR checkout lives elsewhere.
