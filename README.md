# dmr-plugin-feishu

External [DMR](https://github.com/seanly/dmr) plugin (HashiCorp **go-plugin** + `net/rpc`) that connects **Feishu/Lark private (p2p) chat** to the DMR agent: inbound messages trigger `RunAgent`, replies go back to the **thread** or **chat**, and `require_approval` is routed using `ApprovalRequest.Tape`. **Group chats are not handled** (messages are ignored).

Chinese design notes: [`docs/implementation.md`](docs/implementation.md), [`docs/architecture.md`](docs/architecture.md).

## Requirements

- Go **1.25+** (matches DMR)
- A checked-out **dmr** repo next to this one (see `go.mod` `replace`) or adjust the replace path
- Feishu app with **WebSocket** mode / `im.message.receive_v1`, plus `app_id`, `app_secret`, `verification_token`, and (if used) `encrypt_key`
- For **`feishu.send_file`**: enable the app permissions needed to **upload IM files** and **send file messages** (see Feishu open platform: IM message / file APIs for your app type)
- For **`feishu.send_text`**: IM **create/send message** permissions for the bot (same family as normal agent replies to p2p chats)

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

Example snippet (`~/.dmr/config.yaml`):

```yaml
plugins:
  - name: opa_policy
    enabled: true

  - name: cli_approver
    enabled: false

  - name: feishu
    enabled: true
    path: ~/.dmr/plugins/dmr-plugin-feishu   # or absolute path to the binary
    config:
      app_id: "cli_xxx"
      app_secret: "xxx"
      verification_token: "xxx"
      encrypt_key: ""                         # optional, if encryption enabled on the app
      allow_from: []                        # empty = all senders; else allowlist of user ids
      approval_timeout_sec: 300
      dedup_ttl_minutes: 10
      # optional — feishu.send_file
      # send_file_max_bytes: 31457280   # default 30 MiB
      # send_file_root: "/safe/read-only/dir"  # if set, path args must stay under this dir
      # optional — Feishu-only instructions prepended to inbound RunAgent user message (see below)
      # extra_prompt: |
      #   When using cron reminders on this tape, always call feishu.send_text with tape_name.
      # extra_prompt_file: prompts/feishu_extra.md   # relative to ~/.dmr/ (config file directory)
```

Plugin `config` is passed through DMR as JSON; field names match the struct tags below. Legacy keys such as `group_trigger` in YAML are ignored if present in the JSON subset DMR sends.

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
| `send_file_max_bytes` | Max bytes for **`feishu.send_file`** uploads (default `31457280` = 30 MiB). |
| `send_file_root` | If non-empty, `path` arguments must resolve under this absolute directory; if empty, paths are restricted to the plugin process **current working directory** at resolve time. |
| `extra_prompt` | Optional multiline text. Combined with `extra_prompt_file` and **prefixed** to each **Feishu inbound** `RunAgent` prompt (before the real user message, separated by `---`). Not an extra LLM API `system` message — DMR still uses global `agent.system_prompt` for the system role. |
| `extra_prompt_file` | Optional path to a UTF-8 file. **Relative paths** are resolved against DMR’s injected **`config_base_dir`** (the directory containing your main `config.yaml`). Loaded at plugin **Init**; content is placed **before** `extra_prompt` when both are set. |

### Feishu-only extra prompt (limitations)

- **Built-in scheduling hint**: Every Feishu inbound `RunAgent` automatically prepends a short fixed reminder (Chinese + English) that cron-triggered runs need **`feishu.send_text`** with **`tape_name`** to reach IM, and mentions **`run_once`**. This is not controlled by `extra_prompt`; it reduces silent mis-delivery when users ask for reminders.
- **Built-in report delivery hint**: A second fixed prefix (Chinese + English) tells the model that **report-style** outputs (assessments, scan dumps, long write-ups) should be written as a **`.md` file** and sent with **`feishu.send_file`**, not pasted into a huge **`feishu.send_text`**. Short replies may still use **`feishu.send_text`** (`markdown=true` when useful). Also not controlled by `extra_prompt`.
- **Tape audit**: DMR records the **full** string passed to `RunAgent` as one `role=user` entry. The prefix (builtin + optional `extra_prompt`) appears in tape history, not only the raw Feishu text. For a cleaner transcript, rely more on DMR’s global **`system_prompt`**.
- **Cron**: Jobs started by **`dmr-plugin-cron`** call the host directly; they **do not** go through this Feishu inbound prefix. Put instructions in the cron job **`prompt`** or in global **`system_prompt`**.
- **External plugins** cannot register DMR’s `SystemPrompt` hook without host/proto changes; optional `extra_prompt` is an additional plugin-side supplement.

## Tool: `feishu.send_file`

The plugin registers **`feishu.send_file`** via `ProvideTools` / `CallTool` (same mechanism as other external-plugin tools).

- **When it works**: Only while DMR is running the agent for a **Feishu-triggered** private-chat job (between `RunAgent` start and end). The tool sends to the same destination as normal agent replies (`reply_in_thread` when the user message was in a thread).
- **Arguments**: Exactly one of **`path`** (local file) or **`content_base64`** (with required **`filename`**). Optional **`caption`** sends a plain-text line first.
- **Path safety**: Resolved paths must lie under `send_file_root` (if set) or under the process **CWD**; `..` escapes are rejected.
- **OPA**: Treat like other sensitive outbound actions (e.g. `fs.write`): add or extend Rego so `feishu.send_file` is **`require_approval`** or **`deny`** by default in your policy bundle; the feishu plugin does not change DMR’s default `default.rego`.

## Tool: `feishu.send_text`

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
