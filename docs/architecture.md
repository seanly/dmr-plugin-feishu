## DMR Feishu Channel (External Plugin) Architecture

**Current implementation (summary):** The plugin is **private chat (p2p) only**; **group** messages are ignored. Tape names are **`feishu:p2p:<chat_id>`** only. Inbound jobs run on a **single global queue** (one worker, strict FIFO across all chats). The plugin does **not** call **`TapeHandoff`**; it always passes **`HistoryAfterEntryID = 0`** to `RunAgent` (DMR uses default last-anchor tape reads). Tool approvals apply only to **`feishu:p2p:`** tapes.

The sections below retain older design discussion (groups, per-thread workers, plugin-driven anchors) for historical context; they are **not** all reflected in the current code.

---

This document originally consolidated decisions for DMR + Feishu integration:
- Inbound Feishu messages trigger the DMR agent.
- Agent output is sent back to the originating Feishu conversation.
- Tool approvals (`require_approval`) for Feishu are **p2p-only** in the current plugin.
- DMR-internal tape anchors remain a **host/core** concern; this plugin does not drive handoff.
- Throughput: **global serialization** of agent runs in the plugin process (not per-thread parallel workers).

---

### 1) Concepts & Terminology

#### 1.1 DMR “topic/threading” (tape anchor boundaries)
In DMR, “topic boundaries” for context slicing are implemented by:
- Writing an anchor entry onto a tape using `Plugin.TapeHandoff(tapeName, name, state)`.
- Running the agent with `RunAgentRequest.HistoryAfterEntryID = <anchorEntryID>`.
- When `HistoryAfterEntryID > 0`, DMR reads tape entries with `id > HistoryAfterEntryID` and converts them into LLM messages (excluding anchor/event records).

Important: This is DMR-internal context windowing. Feishu does not need to “know” DMR’s anchor/topic boundaries.

#### 1.2 Feishu “thread” (IM native message thread)
Feishu has its own concept of native message threads (话题/线程回复). Whether we reply inside the thread is decided by Feishu inbound event metadata:
- If the trigger message belongs to a Feishu thread, replies go to that same thread.
- Otherwise replies go to the main chat.

---

### 2) Plugin Deployment Model (go-plugin / net/rpc)

`dmr-plugin-feishu` is an **external plugin process** using HashiCorp `go-plugin` and net/rpc:
- Feishu events are handled inside the plugin process.
- The plugin calls the DMR host via reverse RPC:
  - `Plugin.RunAgent` to execute the agent loop against the host.
  - (Optional, if needed) `Plugin.TapeHandoff` to write tape anchors before/for the run.

---

### 3) Inbound Event Flow (Feishu -> DMR)

#### 3.1 WebSocket / P2MessageReceiveV1 handler
The plugin listens to Feishu group message receive events (WebSocket mode):
- Extract:
  - `chatID` (group conversation id)
  - `senderID` (feishu user id)
  - `content` (message text)
  - `triggerMessageID` (`event.Event.Message.MessageId`)
  - Thread membership and a **stable thread key** (see 3.2)
  - `messageID` for deduplication (use the event’s message id or request id depending on what you reliably have)

#### 3.2 Thread membership & stable thread key
We need:
- `inThread` (whether the trigger message belongs to a Feishu thread)
- `threadRootKey` (stable identifier for the thread, used for per-thread isolation)

You must implement extraction from the Feishu event payload using whatever fields the SDK exposes.
If a stable thread root cannot be extracted, per-thread tape isolation will degrade.

#### 3.3 Trigger & filtering policy
**Current:** Only `chat_type == p2p` is accepted; all other chat types return early (no enqueue). For p2p, `allow_from` still filters senders.

Historical note — group chat used to be gated by config (e.g. `allow_from` and `group_trigger.prefixes`); that path has been removed.

---

### 4) De-duplication

Inbound events must be de-duplicated to avoid running multiple agents for the same Feishu message:
- Use a `messageID` key (or request id + message id if needed).
- Keep a bounded in-memory LRU or time-based map to forget old ids.

---

### 5) Concurrency & Throughput (the key performance decision)

#### 5.1 Serialization key: per-thread
To avoid tape interleaving and to keep the reply mapping correct:
- Use a serialization key: `threadKey` (derived from `threadRootKey`, or fallback to trigger message id if threadRootKey is unavailable).
- Run **at most one** agent at a time per `threadKey`.
- Different threads can run concurrently.

#### 5.2 Tape isolation: per thread independent tape (most robust)
For maximum isolation and throughput:
- Use an independent `tapeName` for each thread:
  - `tapeName = feishu:<chatType>:<chatID>:thread:<threadRootKey>`
  - for example:
    - group thread: `feishu:group:<chatID>:thread:<threadRootKey>`
    - p2p: `feishu:p2p:<peerOrChatID>`

Rationale:
- Even if multiple threads exist in the same group chat, they won’t share the same tape.
- This prevents the most common “context pollution” issue.

#### 5.3 Prompt sender prefix
To let the model understand multi-person conversations inside a thread/tape:
- Prefix user content as:
  - `[sender=<feishu_user_id>] <content>`

---

### 6) DMR Agent Invocation (Feishu -> RunAgent)

For each trigger message, the plugin calls:
- `Plugin.RunAgent` with:
  - `TapeName = <computed tapeName>`
  - `Prompt = "[sender=<senderID>] <content>"`
  - `HistoryAfterEntryID` depending on anchor strategy.

---

### 7) Context Isolation with Tape Anchors (anchor strategy)

We use tape anchors to control what the model reads from tape.

#### 7.1 “Strict-after” isolation (strongest, token cost can be higher)
Mechanism:
- Before every run, create a fresh anchor for that trigger.
- Set `HistoryAfterEntryID` to that anchor’s `AnchorEntryID`.

Effect:
- Model reads only after the anchor.
- Most isolation, but may reduce conversational continuity.

#### 7.2 “Periodic / moved anchor” strategy (chosen to prevent token blow-up)
To avoid ever-growing contexts:
- Do not write an anchor for every trigger.
- Instead, periodically move the context window boundary.

Chosen trigger condition:
- When `PromptTokens >= ContextBudget * 0.8`, then:
  1) write a new anchor (`TapeHandoff`)
  2) update the thread’s next `HistoryAfterEntryID` to the new anchor

Implementation detail (plugin state per thread):
- Maintain:
  - `lastHistoryAnchorEntryID`
  - `moveOnNext` flag
- On each run:
  - if `moveOnNext` is true: call `TapeHandoff`, update `HistoryAfterEntryID`
  - else: reuse `lastHistoryAnchorEntryID`
- On each run completion:
  - if `PromptTokens >= ContextBudget*0.8`: set `moveOnNext=true` for the next trigger

Note: DMR itself also performs proactive compaction based on token usage (`shouldAutoHandoff` + compact/auto-handoff). The plugin anchor strategy is complementary.

---

### 8) Outbound Reply Strategy (DMR -> Feishu)

After the agent returns `resp.Output`:
- If `inThread == true`:
  - Reply to the trigger message **inside the same Feishu thread**.
  - Use Feishu’s “message reply in thread” endpoint:
    - `POST /im/v1/messages/{message_id}/reply` with `reply_in_thread=true`
  - The target `message_id` is the trigger message’s `triggerMessageID`.
- If `inThread == false`:
  - Send a normal message to the main chat (`ReceiveId = chatID`).

Approval messages follow the same routing rule:
- thread -> thread
- main -> main

---

### 9) Tool Approval Policy (`require_approval`) for Group vs Single

**Current plugin:** Only **`feishu:p2p:`** tapes exist for this channel. The Feishu approver **denies** any `Tape` that does **not** have that prefix (defensive; group tapes are no longer created).

Historical requirements:
- Group chat does **not** support approvals.
- Single chat supports approvals.

Chosen behavior:
1. **Group** + `require_approval`:
   - Directly return Denied (no approval UI).
2. **Single** + `require_approval`:
   - Request approval and show UI (web/cli) as configured.
   - Approval prompt is sent back to the same private chat.

Implementation approach:
- Adjust DMR protocol so that the approval request sent to `feishu_approver` includes `Tape`.
- `feishu_approver` reads `Tape` prefix to determine group vs single:
  - `feishu:group:` => Denied
  - `feishu:p2p:`   => Request approval UI

---

### 10) Required Protocol Changes (documented, to be implemented in code)

To support the “approval routing by tapeName prefix” without changing the approval UI logic, we add a single field:

#### 10.1 Internal struct change (DMR side)
- `pkg/plugin/approval.go`
  - `type plugin.ApprovalRequest` adds:
    - `Tape string`

#### 10.2 RPC proto change (external plugin boundary)
- `pkg/plugin/proto/types.go`
  - `type proto.ApprovalRequest` adds:
    - `Tape string`

#### 10.3 RPC wiring
- `pkg/plugin/external.go`
  - `ExternalApprover.RequestApproval` and `RequestBatchApproval` must copy `req.Tape` into `proto.ApprovalRequest.Tape`.

#### 10.4 OPA policy injection
- `plugins/opapolicy/opapolicy.go`
  - When `require_approval` is triggered:
    - For single approval: set `Tape: toolCtx.Tape`
    - For batch approval: set `Tape: item.Ctx.Tape` for each pending request

External approver behavior:
- `dmr-plugin-web` / `cli_approver` should compile with the added field.
- `feishu_approver` uses `Tape` prefix to decide group vs single behavior.

---

### 11) Configuration (high-level)

Feishu plugin configuration should include:
- Feishu app credentials:
  - `app_id`, `app_secret`
  - `verification_token`, `encrypt_key`
- `allow_from` to filter which **p2p** senders may trigger the agent (`group_trigger` removed; group chat is ignored)
- Optional approval timeout / routes if needed by the web/cli UI

---

### 12) Self-Consistency Checks (what must always hold)

1. For each trigger message, the DMR output must be returned to the same conversation target:
   - thread reply if `inThread`
   - main chat otherwise
2. Approval routing: only **`feishu:p2p:`** tapes are approved in-channel; any other prefix is denied.
3. **Global** queue ensures at most one in-flight `RunAgent` in this plugin process (FIFO across all p2p chats).

