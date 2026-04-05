# Feishu Group Chat Support Design

> **Date**: 2026-04-05
> **Status**: Proposed

## Overview

Support group chat (群聊) in Feishu plugin with thread isolation and admin-based approval routing.

## Tape Naming

| Scenario | Tape Format | Description |
|----------|-------------|-------------|
| **P2P private chat** | `feishu:p2p:<chat_id>` | Existing behavior, unchanged |
| **Group chat normal message** (no thread) | `feishu:p2p:<sender_id>` | Reuses P2P tape, merged history |
| **Group chat thread** (has thread_id) | `feishu:group:<chat_id>:<bot_id>:thread:<thread_id>` | Independent tape per thread |

## Message Processing Flow

```
Group message received
    │
    ├─ chat_type = "group"
    │
    ├─ Check mentions[] — is this bot mentioned?
    │   └─ No → Ignore
    │   └─ Yes → Continue
    │
    ├─ Extract sender_id (open_id / user_id / union_id)
    │
    ├─ Check thread_id exists?
    │   ├─ No (normal group message):
    │   │   └─ Tape = feishu:p2p:<sender_id>
    │   │   └─ Use p2p reply logic
    │   │
    │   └─ Yes (thread/topic):
    │       └─ Tape = feishu:group:<chat_id>:<bot_id>:thread:<thread_id>
    │       └─ Reply in thread
    │
    └─ Enqueue job for processing
```

## Approval Routing

All approvals go to **bot admin's P2P private chat**, not to the group or triggering user.

### Admin Identification

- Admin = `allow_from[0]`
- Admin must have previously messaged the bot privately → bot establishes `chat_id` route

### Approval Flow

```
Group message triggers approval
    │
    ├─ Get admin's chat_id from established routes (allow_from[0])
    │
    ├─ Chat_id exists?
    │   ├─ Yes → Send approval to admin's P2P chat
    │   │
    │   └─ No (admin never P2P'd the bot) → Deny approval
    │
    └─ Admin responds in P2P (y/s/a/n)
```

## Approval Targets

| Scenario | Approval Sent To |
|----------|-----------------|
| P2P private chat | Triggering user's P2P chat (`feishu:p2p:<chat_id>`) |
| Group chat normal message | Admin's P2P chat |
| Group chat thread | Admin's P2P chat |

## Reply Logic

- **In-thread** (has thread_id): Reply to thread via `Message.Reply` with `reply_in_thread=true`
- **Not in thread**: Reply to main chat via `Message.Create`

## Queue Strategy

Per-tape queue (upgrade from per-chat_id):
- `workers map[string]chan *inboundJob // tapeName -> job channel`
- Different tapes (different bots/threads) run in parallel
- Same tape runs serially

## Key Implementation Changes

### 1. Remove P2P-only guard
In `handleMessageReceive()`:
```go
// REMOVE this:
if chatType != "p2p" {
    log.Printf("feishu: ignoring non-p2p message")
    return nil
}
```

### 2. Add mention detection
Parse `mentions[]` in message content to check if bot is mentioned:
```go
// Check if bot's open_id is in mentions
func isBotMentioned(content, botOpenID string) bool {
    // Parse mentions array from content
    // Return true if botOpenID found
}
```

### 3. Add tape naming for group
```go
func tapeNameForGroup(chatID, botOpenID string, threadID string) string {
    if threadID != "" {
        return fmt.Sprintf("feishu:group:%s:%s:thread:%s", chatID, botOpenID, threadID)
    }
    // Normal group message → reuse p2p tape with sender
    return fmt.Sprintf("feishu:p2p:%s", senderID)
}
```

### 4. Update approver for admin routing
```go
func (a *FeishuApprover) handleSingle(req *proto.ApprovalRequest, resp *proto.ApprovalResult) {
    tape := strings.TrimSpace(req.Tape)

    if strings.HasPrefix(tape, "feishu:group:") {
        // Group chat → send to admin's P2P chat_id
        adminChatID := a.getAdminChatID()
        if adminChatID == "" {
            resp.Choice = choiceDenied
            resp.Comment = "admin has no P2P chat established"
            return
        }
        // ... send to adminChatID
        return
    }

    // P2P → existing logic
    chatID, ok := p2pChatIDFromTape(tape)
    // ...
}

func (a *FeishuApprover) getAdminChatID() string {
    if len(a.bot.cfg.AllowFrom) == 0 {
        return ""
    }
    adminID := a.bot.cfg.AllowFrom[0]
    // Look up chat_id from established routes
    return a.plugin.getChatIDForSender(adminID)
}
```

### 5. Add route for sender_id → chat_id
Need to track `sender_id -> chat_id` mapping for admin lookup:
```go
// In FeishuPlugin
routingBySender map[string]string // sender_id -> chat_id (for admin lookup)
```

## Out of Scope

- Group chat without @mention activation (bot ignores all group messages)
- Multiple admins (only first `allow_from` entry is admin)
- Admin changing mid-session

## Related Docs

- `architecture.md` — Historical design context (groups were previously considered)
- `implementation.md` — Current p2p-only implementation details
