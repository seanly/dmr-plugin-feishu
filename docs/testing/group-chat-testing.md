# Feishu 群聊功能测试指南

## 测试前准备

### 1. 获取必要信息

```bash
# 1. 先启用私聊功能，发送 ",id" 或 ",openid" 命令获取你的 open_id
# 2. 把机器人拉入一个测试群
# 3. 在群里 @机器人 并发送 ",id" 命令，获取群的 chat_id
```

### 2. 配置 DMR

编辑 `~/.dmr/config.toml`：

```toml
[[plugins]]
name = "feishu"
enabled = true
path = "~/.dmr/plugins/dmr-plugin-feishu"

[plugins.config]
# 基础配置
app_id = "cli_xxx"
app_secret = "xxx"
verification_token = "xxx"
allow_from = ["ou_your_open_id"]  # 你的 open_id

# 群聊功能开关
group_enabled = true

# 群聊审批管理员（需要配置，否则群聊审批会失败）
[[plugins.config.admins]]
open_id = "ou_your_open_id"  # 管理员的 open_id（可以是你自己）
name = "测试管理员"

# Bot 的 open_id（用于 @mention 检测）
[[plugins.config.bots]]
app_id = "cli_xxx"
app_secret = "xxx"
bot_open_id = "ou_bot_xxx"  # 从 Feishu 开发者后台获取
```

### 3. 启动 DMR

DMR 通过 brain 模式运行，启动方式：

```bash
# Brain 模式（推荐测试使用）
dmr brain --model $MODEL --api-key $KEY

# 注意：DMR 启动后会加载插件，Feishu 插件会在后台运行 WebSocket 监听消息
# 查看日志需要在 DMR 启动时观察控制台输出
```

---

## 测试用例

### 测试 1：group_enabled = false（默认行为）

**目的**：验证关闭群聊功能时，群消息被忽略

**步骤**：
1. 设置 `group_enabled = false`
2. 重启 DMR
3. 在群里 @机器人 发送消息

**预期结果**：
```
# DMR 日志应显示：
feishu: group message ignored (group_enabled=false)
```

机器人不应响应任何群聊消息。

---

### 测试 2：@mention 检测

**目的**：验证必须 @机器人才响应

**步骤**：
1. 设置 `group_enabled = true`
2. 重启 DMR
3. 在群里**不 @机器人**，直接发送 "hello"
4. 在群里**@机器人**，发送 "hello"

**预期结果**：
```
# 步骤 3 日志：
feishu: group message ignored (bot not mentioned)

# 步骤 4 日志：
feishu: group message received chatID=xxx threadID= senderID=xxx ...
feishu: enqueue group job tape=feishu:group:xxx:main queue=feishu:group:xxx:main ...
```

---

### 测试 3：@all 忽略

**目的**：验证 @all 不会触发机器人响应

**步骤**：
1. 在群里 @所有人 并附加消息
2. 查看 DMR 日志

**预期结果**：
```
feishu: group message ignored (@all)
```

---

### 测试 4：话题隔离

**目的**：验证不同话题有独立的 tape 和上下文

**步骤**：
1. 在群里创建两个不同的话题（Thread A 和 Thread B）
2. 在 Thread A 中 @机器人："记住我的名字是 Alice"
3. 在 Thread B 中 @机器人："我的名字是什么"
4. 在 Thread A 中 @机器人："我的名字是什么"

**预期结果**：
```
# Thread A 的 tape：
feishu:group:oc_xxx:thread:thread_id_a

# Thread B 的 tape：
feishu:group:oc_xxx:thread:thread_id_b

# Thread B 的回复应该不知道名字（独立上下文）
# Thread A 的回复应该知道是 Alice
```

---

### 测试 5：群聊审批路由

**目的**：验证群聊中的危险操作审批发送到管理员 P2P

**前置条件**：
- 配置 `admins` 列表
- OPA policy 配置某个工具需要审批（如 `shell` 或 `fsWrite`）

**步骤**：
1. 在群里 @机器人："执行 shell 命令 ls -la"
2. 查看管理员（配置的 admin）的私聊

**预期结果**：
```
# DMR 日志：
feishu: group approval requested, routing to admin: ou_admin_xxx

# 管理员私聊收到审批消息：
## DMR tool approval required
- **Tool:** `shell`
- **Risk:** ...
...
```

---

### 测试 6：审批回复处理

**目的**：验证管理员在私聊回复后，群聊操作继续执行

**步骤**：（承接测试 5）
1. 管理员在私聊回复 `y`（或 `yes`、`s`、`a`、`n`）
2. 查看群聊是否收到执行结果

**预期结果**：
```
# 管理员回复后，群聊收到：
<命令执行结果>
```

---

### 测试 7：feishuSendText 在群聊中

**目的**：验证工具能在群聊/话题中发送消息

**步骤**：
1. 在群里 @机器人："发送消息 '测试消息' 到这个群"
2. 或者在话题中 @机器人："回复这个话题说 '收到'"

**预期结果**：
- 消息发送到正确的群或话题中

---

### 测试 8：feishuSendFile 在群聊中

**目的**：验证文件发送在群聊中工作

**步骤**：
1. 在群里 @机器人："读取 /tmp/test.txt 并发送到这里"
2. 确保文件存在且有内容

**预期结果**：
- 文件成功发送到群聊

---

### 测试 9：队列并行性

**目的**：验证不同话题并行处理

**步骤**：
1. 同时在话题 A 和话题 B @机器人，发送需要长时间处理的任务
2. 观察处理时间

**预期结果**：
```
# 两个话题同时处理，不会互相等待
feishu: enqueue group job tape=feishu:group:xxx:thread:A ...
feishu: enqueue group job tape=feishu:group:xxx:thread:B ...
# 两个 RunAgent 几乎同时启动
```

---

### 测试 10：未配置管理员

**目的**：验证未配置管理员时群聊审批被拒绝

**步骤**：
1. 清空或注释掉 `admins` 配置
2. 在群里触发需要审批的操作

**预期结果**：
```
feishu: group approval requested but no admins configured
# 操作被拒绝
```

---

## 调试技巧

### 1. 查看详细日志

```bash
# 启动 DMR brain 模式，查看实时日志
dmr brain --model $MODEL --api-key $KEY 2>&1 | grep -E "(feishu|group|approval)"

# 或者在另一个终端查看 DMR 日志文件（如果配置了日志输出）
tail -f ~/.dmr/dmr.log | grep -E "(feishu|group|approval)"
```

### 2. 验证 Tape 命名

```bash
# 在 DMR 主机上查看 tape 文件
ls -la ~/.dmr/tapes/ | grep feishu:group

# 应该看到：
# feishu:group:oc_xxx:main
# feishu:group:oc_xxx:thread:xxx
```

### 3. 检查配置加载

```bash
# 启动 DMR 时查看控制台输出，确认插件加载成功
# 应该看到：
feishu: initialized 1 bots
feishu: bot #0 bot_open_id=ou_xxx  # 如果有这行，说明 bot_open_id 配置正确
```

### 4. 手动测试 API

```bash
# 测试 bot_open_id 是否正确
# 在群里 @机器人，如果日志显示：
# feishu: group message received ...
# 说明 @mention 检测成功

# 如果没有，检查：
# 1. bot_open_id 是否正确
# 2. 群里是否真的有这个机器人
```

---

## 常见问题

### Q: 群里 @机器人没反应

**排查步骤**：
1. 检查 `group_enabled = true`
2. 检查 `bot_open_id` 是否正确
3. 查看日志是否有 `bot not mentioned`
4. 确认机器人已在群里（不是仅在私聊）

### Q: 审批没有发送到管理员

**排查步骤**：
1. 检查 `admins` 配置是否包含有效的 `open_id`
2. 确认管理员和机器人是好友关系
3. 查看日志是否有 `routing to admin`

### Q: 话题上下文没有隔离

**排查步骤**：
1. 查看 tape 名称是否包含 `thread:`
2. 检查不同话题的 thread_id 是否不同

---

## 测试检查清单

- [ ] group_enabled = false 时忽略群消息
- [ ] group_enabled = true 时响应 @机器人
- [ ] 不 @机器人时忽略消息
- [ ] @all 时被忽略
- [ ] 不同话题有独立的 tape
- [ ] 话题内回复保持在话题内
- [ ] 群聊审批发送到管理员 P2P
- [ ] 管理员审批后群聊收到结果
- [ ] feishuSendText 在群聊工作
- [ ] feishuSendFile 在群聊工作
- [ ] 多话题并行处理
