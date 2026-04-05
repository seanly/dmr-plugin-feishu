# TODO

## Completed ✅

- [x] Implement Feishu group chat support
  - Design: ./group-chat-design.md
  - Implementation: internal/inbound/receiver.go, internal/inbound/group.go
  - Features:
    - @mention detection (bot-specific, @all ignored)
    - Thread-aware tape naming (per-thread queue)
    - Admin-based approval routing (P2P instead of group)
    - Mention text stripping

- [x] Multi-bot instance support
  - Dynamic routing: chat_id -> bot instance
  - Backward compatible with single-bot config

- [x] WebSocket auto-reconnection
  - Exponential backoff: 5s ~ 5min
  - Fresh client recreation on each retry

- [x] Job lifecycle improvements
  - Delayed cleanup (30s) for async/subagent tool calls
  - Job ID tracking to prevent race conditions

- [x] Configuration documentation
  - TOML structure with examples
  - Design decisions documented

## In Progress 🚧

- [ ] Add unit tests for core components
  - [ ] Config parsing (ParseConfig)
  - [ ] Job management (SetActiveJob, ClearActiveJob)
  - [ ] Tape name parsing (P2PChatIDFromTape, GroupChatIDFromTape)
  - [ ] Mention detection (CheckMention)
  - [ ] Message deduplication (Deduper)

## Backlog 📋

- [ ] Metrics and monitoring
  - Prometheus metrics for message count, latency, errors
  - WebSocket connection health
  - Queue depth monitoring

- [ ] Configuration hot reload
  - Watch config file changes
  - Reload without restart

- [ ] Message persistence
  - Persist job state across restarts
  - Support distributed deployment

- [ ] Enhanced error handling
  - Structured error codes
  - Better retry strategies for transient failures

- [ ] Performance optimizations
  - Connection pooling for HTTP requests
  - Batch message processing
