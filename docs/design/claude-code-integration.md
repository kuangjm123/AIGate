# Claude Code 接入与复现指南

本文说明 magpie 如何接入 Claude Code，以及另一个项目怎样复现同样的两条主要链路：

1. **Claude Code 作为客户端**：把 Claude Code 的 Anthropic 请求指向自己的网关。
2. **网关把 Claude 订阅交给真实 Claude Code**：网关收到 Claude 订阅账号的请求后，启动本机 `claude` 进程，而不是直接回放 OAuth token。

另有一条独立启动路径，适合“保留原来的 `claude`，另起一个只走网关的 Claude Code”：

3. **独立 settings 启动**：写一份自己的 settings 文件，用 `claude --settings <file>` 启动，不改用户的 `~/.claude/settings.json`。

本文依据 magpie 当前实现整理，目标是让另一个 coding agent 能照着落地。它不是 magpie 子系统的行为契约；如果改的是 magpie 本身，仍应更新对应的 [子系统参考](subsystems/README.md)。

## 1. 先选模式

| 你的目标 | 需要做的事 | 是否需要真实 Claude Code 进程 |
| --- | --- | --- |
| 让 Claude Code 使用你的模型/网关 | 改 Claude Code 的 settings，提供 Anthropic Messages 路由 | 否 |
| 把你的 Claude 订阅暴露成网关模型 | 启动真实 `claude`，桥接 stream-json 和工具调用 | 是 |
| 不污染用户原来的 Claude Code | 独立 settings 文件 + `claude --settings` | 否 |
| 尽量贴近订阅额度计费方式 | 在桥接层实现 Claude Code 交互式模式 | 是，且复杂度最高 |

如果只是“让 Claude Code 接进来”，先做模式 1。只有确实要代理 Claude 订阅账号时，才做模式 2。

## 2. 总体架构

### 2.1 Claude Code 作为客户端

```text
Claude Code CLI
  -> POST /v1/messages
  -> 你的网关
  -> API key 供应商 / 本地模型 / 订阅桥
```

Claude Code 自己会拼 `/v1/messages`，所以 `ANTHROPIC_BASE_URL` 应该是网关根地址：

```text
http://127.0.0.1:3425
```

不是：

```text
http://127.0.0.1:3425/v1
```

### 2.2 网关驱动真实 Claude Code

```text
POST /v1/messages
  -> 网关识别 Claude 订阅账号
  -> 启动/复用 claude -p 进程
  -> Claude Code 自己访问 Anthropic
  -> 网关把 stream-json 翻译回 Anthropic SSE/JSON
```

调用方的工具不会在网关本机执行。网关把它们作为 MCP 工具交给 Claude Code；Claude Code 调用时，MCP helper 回调网关，网关再把 `tool_use` 交给调用方。调用方下一次请求带回 `tool_result`，同一个 Claude Code 进程继续这一轮。

### 2.3 关键状态索引

一个可用的桥接器至少要维护四张表：

| 索引 | 用途 |
| --- | --- |
| `callback token -> run` | MCP helper 回调时找到对应的 Claude Code 进程 |
| `tool_use id -> run` | 调用方带回 `tool_result` 时找到等待结果的进程 |
| `conversation key -> idle run` | 下一轮对话复用同一个 Claude Code 进程 |
| `session id -> saved session` | 进程退出后，用 `--resume` 接着之前的会话 |

只用“每个请求启动一个新进程”也能跑通文本对话，但会反复把完整历史写给 Anthropic，成本和缓存都会明显变差。进程复用是完整实现的一部分。

## 3. 模式 A：Claude Code 接入网关

### 3.1 配置文件位置

Claude Code 的 settings 通常在这些位置之一：

```text
$CLAUDE_CONFIG_DIR/settings.json
~/.claude/settings.json
```

magpie 的 `appdir.ClaudeDir()` 会优先读 `CLAUDE_CONFIG_DIR`，否则用 `~/.claude`。Claude Code 还可能读取项目级 `.claude/settings.json`、`.claude/settings.local.json`、`~/.claude.json`，以及管理员管理的 managed settings。写配置前要确认最终生效的是哪一层，不能假设只有 `~/.claude/settings.json`。

### 3.2 最小配置

把模型 id 替换成你网关 `/v1/models` 或目录里的真实 id：

```json
{
  "model": "provider/main-model",
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:3425",
    "ANTHROPIC_AUTH_TOKEN": "magpie-claude",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "provider/opus-model",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "provider/sonnet-model",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "provider/haiku-model",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "provider/fable-model",
    "ANTHROPIC_SMALL_FAST_MODEL": "provider/haiku-model",
    "CLAUDE_CODE_SUBAGENT_MODEL": "provider/main-model"
  }
}
```

如果只关心一个模型，至少写：

```json
{
  "model": "provider/main-model",
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:3425",
    "ANTHROPIC_AUTH_TOKEN": "magpie-claude"
  }
}
```

但这样 Claude Code 的 `opus`、`sonnet`、`haiku`、`fable` 别名仍会按它自己的内置映射解析，可能请求到网关不提供的 Anthropic 模型。多档位场景应显式写 tier 变量。

### 3.3 字段含义

| 字段 | 作用 | 注意事项 |
| --- | --- | --- |
| `env.ANTHROPIC_BASE_URL` | Anthropic API 根地址 | 不含 `/v1`；不要带末尾 `/v1/messages` |
| `env.ANTHROPIC_AUTH_TOKEN` | 以 `Authorization: Bearer` 发送的网关 key | 也可用 `ANTHROPIC_API_KEY`，但两者不要混用 |
| 顶层 `model` | Claude Code 当前主模型 | 可以是网关模型 id；`/model` 会改它 |
| `ANTHROPIC_DEFAULT_OPUS_MODEL` | `/model opus` 使用的模型 | 显式设置，避免回落到 Anthropic 内置 id |
| `ANTHROPIC_DEFAULT_SONNET_MODEL` | `/model sonnet` 使用的模型 | 同上 |
| `ANTHROPIC_DEFAULT_HAIKU_MODEL` | `/model haiku` 使用的模型 | 也常用于轻量任务 |
| `ANTHROPIC_DEFAULT_FABLE_MODEL` | `/model fable` 使用的模型 | 当前 Claude Code 支持该档位 |
| `ANTHROPIC_SMALL_FAST_MODEL` | 后台/轻量任务的兼容变量 | 建议与 haiku 档位一致 |
| `CLAUDE_CODE_SUBAGENT_MODEL` | 子 agent 的模型 | 不设置则跟随会话或 Claude Code 默认 |
| `CLAUDE_CODE_EFFORT_LEVEL` | 会话思考档位 | 会压过 `/effort`；除非明确要锁死，不要写 |
| `ANTHROPIC_MODEL` | 环境级主模型 | **不要写**；它会压过 settings 的 `model` 和 `/model` |

`magpie-claude` 这种 key 在网关只监听 loopback 时通常可以直接用；如果网关开了局域网共享，要换成用户配置的真实 gateway key，否则请求会被拒绝。

### 3.4 为什么不要写 `ANTHROPIC_MODEL`

Claude Code 的 `ANTHROPIC_MODEL` 优先于 settings 里的 `model`。一旦写入，用户在 Claude Code 里通过 `/model` 选的模型只在会话内有效，下一次启动又会被环境变量改回去。magpie 在接线时会主动删除这个键：

```text
env.ANTHROPIC_MODEL  -> 删除
model                -> 由 magpie 写
```

如果你的项目从旧版本迁移，连接前要把用户原来的 `ANTHROPIC_MODEL` 记下来，断开时再恢复。

### 3.5 tier 和模型别名

Claude Code 的档位是：

```text
opus
sonnet
haiku
fable
```

`/model opus` 会解析到 `ANTHROPIC_DEFAULT_OPUS_MODEL`，其他同理。顶层 `model` 可以是具体模型 id，也可以是 `opus` 这样的别名。为了减少歧义，建议写具体模型 id；但用户手动 `/model opus` 后，Claude Code 会保存别名或档位值，这是正常行为。

如果模型有额外的显示名和描述，Claude Code 还支持：

```text
ANTHROPIC_DEFAULT_<TIER>_MODEL_NAME
ANTHROPIC_DEFAULT_<TIER>_MODEL_DESCRIPTION
ANTHROPIC_DEFAULT_<TIER>_MODEL_SUPPORTED_CAPABILITIES
```

这些是可选的 UI 信息。写了就要和 tier 模型一起更新，否则 `/model` 会显示过期名称。

### 3.6 1M 上下文标记

Claude Code 用模型 id 末尾的 `[1m]` 表示 1M 上下文，例如：

```text
provider/some-model[1m]
```

magpie 对 1M 窗口的模型会加这个标记，否则 Claude Code 可能按 200K 处理，长对话会提前报 “Prompt is too long”。

复现时要注意：

- `[1m]` 只是给 Claude Code 的标记，网关解析模型时必须先去掉它，再查目录和转发上游。
- 不是所有 Anthropic 模型都适合加 `[1m]`；没有 1M 权益的订阅账号可能报 “Usage credits required for 1M context”。
- 如果模型 id 本身带有 `[1m]`，不要重复添加。
- 可选地支持 magpie 的 `<model>:<effort>[1m]` 约定；这是 magpie 自己的扩展，不是 Claude Code 原生协议，网关要负责拆分。

### 3.7 云厂商开关会绕过网关

Claude Code 支持这些云厂商开关：

```text
CLAUDE_CODE_USE_BEDROCK
CLAUDE_CODE_USE_VERTEX
CLAUDE_CODE_USE_FOUNDRY
CLAUDE_CODE_USE_ANTHROPIC_AWS
CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD
CLAUDE_CODE_USE_MANTLE
```

任意一个为真时，Claude Code 可能使用自己的云厂商 endpoint，绕过 `ANTHROPIC_BASE_URL`。这些开关可能来自：

- shell 环境
- `~/.claude.json`
- 用户 `settings.json` 的 `env`
- managed settings

连接时要把它们从你控制的 settings 层里删掉，或者显式写成空字符串。写在你控制的 settings `env` 里的空字符串可以压过 shell；如果来自 managed settings，通常无法覆盖，要在 UI/日志里明确告诉用户“请求不会经过网关”。

同样要处理 `ANTHROPIC_API_KEY`：

- 它可能被 Claude Code 作为 `x-api-key` 发送。
- 如果网关只认 `ANTHROPIC_AUTH_TOKEN`，保留它会造成多余或冲突的凭据。
- 独立启动模式建议在启动文件里把它设为 `""`，把用户的真实 API key 留给用户自己的 `claude`。

### 3.8 网关侧必须提供什么

Claude Code 主要使用：

```text
POST /v1/messages
POST /v1/messages/count_tokens
```

magpie 同时兼容 `POST /messages`，但复现时可以只实现 `/v1/messages`；如果目标客户端可能省略 `/v1`，再加别名。

`/v1/messages` 要做 Anthropic Messages 兼容：

- 接受 `Authorization: Bearer <key>` 或 `x-api-key`。
- 解析 `model`、`system`、`messages`、`tools`、`tool_choice`、`stream`、`max_tokens`、`thinking`。
- Claude Code 2.1.x 可能在 `messages` 数组中发送 `role: "system"` 的环境上下文；跨协议转换时应把它当作 system/developer instructions，而不是普通对话轮次拒绝。
- 支持流式 SSE：

```text
message_start
content_block_start
content_block_delta*
content_block_stop
message_delta
message_stop
```

- 支持 `text`、`tool_use`、`tool_result`、`thinking` 内容块。
- 把 `stop_reason` 如实返回：`end_turn`、`tool_use`、`max_tokens`、`refusal` 等。
- 工具调用时先返回 `tool_use`，不要等工具执行完。
- 长思考或工具等待期间发送 SSE 注释心跳，避免代理/客户端超时。
- 流已经开始后出错时，发送 Anthropic 形状的 `error` 事件，再关闭连接。

`/v1/messages/count_tokens` 返回：

```json
{ "input_tokens": 1234 }
```

不需要精确到 Anthropic 的 tokenizer；但不要为了计数拿订阅 OAuth token 直接请求 Anthropic。Claude 订阅桥的计数应本地估算。

### 3.9 连接、断开和恢复算法

不要“看见 settings.json 就直接改”。建议按下面的顺序实现：

1. 解析 settings 文件；解析失败表示“未知”，不是空文件，不能覆盖。
2. 记录所有将要修改的键：是否存在、原值是什么、你准备写成什么。
3. 检查 managed settings、`.claude.json`、shell 里的云厂商开关和 `ANTHROPIC_BASE_URL`，判断最终是否真的会走网关。
4. 只写必要的键，保留用户的其它 settings、MCP server、hooks、permissions。
5. 断开时逐个键判断：
   - 当前值仍然等于你写入的值：恢复原值；原来不存在就删除。
   - 当前值已经被用户改过：不要覆盖用户的新值。
6. 连接状态和快照要落盘，进程重启后还能正确断开。

一个快照可以长这样：

```json
{
  "path": "C:/Users/me/.claude/settings.json",
  "keys": {
    "env.ANTHROPIC_BASE_URL": { "present": false },
    "env.ANTHROPIC_AUTH_TOKEN": { "present": true, "value": "old-token" },
    "model": { "present": true, "value": "claude-sonnet-4-6" }
  },
  "written": {
    "env.ANTHROPIC_BASE_URL": "http://127.0.0.1:3425",
    "env.ANTHROPIC_AUTH_TOKEN": "magpie-claude",
    "model": "provider/main-model"
  }
}
```

#### `/model` 的特殊情况

Claude Code 可能把用户在 `/model` 里的选择写回用户 settings，而不是写到 `--settings` 文件。magpie 的 `ClaudeLaunchAfter` 处理了这个问题：

- 如果用户选的是只由网关提供的模型，把它留在启动文件里，供下次 `claude-magpie` 使用。
- 用户原来的 `~/.claude/settings.json` 里的 `model` 恢复原值，避免普通 `claude` 去请求一个它访问不到的模型。
- 如果用户选的是 Anthropic 原生模型，普通 `claude` 也能用，可以保留。

移植时至少要做到：不要在用户退出后把“只有你的网关能解析的模型 id”留在用户的主 settings 里。

### 3.10 不污染用户配置：独立 settings

最稳妥的接入方式不是改 `~/.claude/settings.json`，而是写自己的文件：

```text
<your-config-dir>/claude-launch/settings.json
```

然后启动：

```text
claude --settings <your-config-dir>/claude-launch/settings.json
```

`--settings` 会按键覆盖用户 settings 和 shell 的 `env`，但不会接管所有 settings 来源。为了隔离开用户 hooks、MCP server 和项目配置，桥接/测试场景可以再加：

```text
--setting-sources ""
--strict-mcp-config
--mcp-config <json>
```

注意：

- `--setting-sources ""` 会连用户自己的 permissions、hooks 一起关掉；适合网关自己启动的 headless 进程，不适合直接替代用户的普通 `claude`。
- managed settings 通常无法这样关掉；要检测并报告。
- 独立启动文件里要把 `ANTHROPIC_API_KEY` 和云厂商开关写成 `""`，否则它们可能从用户 settings 或 shell 漏进来。
- 文件权限设为 `0600`，不要把 token 写进命令行。

magpie 的 `magpie claude run [args...]` 就是这条路线：

1. 生成 `<config>/claude-launch/settings.json`。
2. 执行 `claude --settings <file> [args...]`。
3. 运行前记录用户主 settings 的 `model`。
4. 运行后处理 `/model` 写入用户 settings 的情况。

### 3.11 代理和网络

Claude Code 基于 Bun，只接受 `http://` 和 `https://` 代理。给它 `socks5://` 会报 `UnsupportedProxyProtocol`。如果用户只有 SOCKS 代理，需要在本机先起一个 HTTP 代理桥。

访问本机网关时，建议把 `127.0.0.1`、`localhost` 放进 `NO_PROXY`，否则请求可能被代理带走。

## 4. 模式 B：网关把 Claude 订阅交给真实 Claude Code

### 4.1 为什么不能直接回放 OAuth token

magpie 的 `internal/gateway/claude_subscription.go` 开头写明了原因：Anthropic 会根据请求内容、客户端身份和入口判断订阅请求，直接把 Claude Code 的 OAuth token 当普通 API key 回放，可能把请求归到错误的用量类别，甚至带来账号风险。

所以这条路线的原则是：

- 只在用户自己的机器上、用用户自己的 Claude Code 登录态运行。
- 让真实 `claude` 进程访问 Anthropic，网关不拿 OAuth token 发 HTTP。
- 不伪造 Claude Code 的入口身份，不把 token 交给第三方。
- 遵守 Anthropic 的服务条款。

如果另一个项目只做“Claude Code 连我的网关”，不需要实现这一节。

### 4.2 找到 Claude Code

最低要求：

```text
exec.LookPath("claude")
```

完整实现还应检查：

- 用户自己的 npm/native/Homebrew 安装位置。
- 当前项目下载并固定版本的 Claude Code。
- Windows 上的 `claude.cmd`，必要时通过 `cmd.exe` 启动。
- WSL 里的 `claude`，通过 `wsl.exe` 启动。
- 不要启动到自己：如果 PATH 上的 `claude` 就是你的程序，直接报错。

自动下载不是必须的。要下载就使用 Anthropic 官方 release：

```text
https://downloads.claude.ai/claude-code-releases/latest
https://downloads.claude.ai/claude-code-releases/<version>/manifest.json
```

校验 manifest 里的 SHA-256 和 size，校验失败不要保留。固定版本并设置 `DISABLE_AUTOUPDATER=1`，否则 CLI 参数行为可能在你不知情时变化。

### 4.3 启动参数

magpie 的 `claudeCLIArgs` 使用：

```text
claude
  -p
  --output-format stream-json
  --input-format stream-json
  --include-partial-messages
  --verbose
  --model <model>
  --tools ""                      # 或 --tools WebSearch
  --strict-mcp-config
  --mcp-config <json>
  --setting-sources ""
  --settings <json-or-file>
  --dangerously-skip-permissions
  --no-session-persistence
```

按需追加：

```text
--effort <low|medium|high|xhigh|max>
--thinking-display summarized
--thinking disabled
--json-schema <schema>
--resume <session-id>
--session-id <uuid>
--system-prompt-file <file>
--append-system-prompt-file <file>
```

这些参数随 Claude Code 版本变化。启动前记录 `claude --version`，对可选参数做 feature detect；不要假设一个很旧的版本支持 `--effort`、`--session-id` 或 `--setting-sources`。

参数解释：

| 参数 | 作用 |
| --- | --- |
| `-p` | headless 模式；stdin/stdout 用 stream-json |
| `--input-format stream-json` | 从 stdin 逐行读用户消息 |
| `--output-format stream-json` | 向 stdout 逐行写事件 |
| `--include-partial-messages` | 输出 Anthropic 形状的增量事件 |
| `--verbose` | stream-json 需要 |
| `--model` | 必须传 Claude Code/Anthropic 能理解的名字 |
| `--tools ""` | 关闭 Claude Code 内置工具 |
| `--strict-mcp-config` | 只使用 `--mcp-config` 里的 MCP server |
| `--mcp-config` | 注入网关的 MCP helper |
| `--setting-sources ""` | 不读用户/项目/本地 settings |
| `--settings` | 注入一份最小 settings |
| `--dangerously-skip-permissions` | 跳过权限确认；只在严格受限环境使用 |
| `--no-session-persistence` | 进程内保存历史，不落盘 |

#### 关于 `--dangerously-skip-permissions`

不要无条件复制这个参数。它只在下面条件同时成立时可以考虑：

- `--tools ""` 已关闭 Claude Code 自己的工具。
- `--strict-mcp-config` 只暴露你的 MCP server。
- MCP 工具调用被交回调用方执行，不在本机执行任意命令。
- 进程运行在沙箱/容器/专用用户下。
- 你清楚 root 用户需要额外处理。

更小权限的替代方案是：

```text
--permission-mode default
--allowedTools mcp__bridge
```

其中 `bridge` 是你的 MCP server 名。具体行为随 Claude Code 版本变化，必须用真实版本验证。

Claude Code 以 root 运行时，通常拒绝 `--dangerously-skip-permissions`，除非环境里有 `IS_SANDBOX=1` 或等价的沙箱标记。magpie 只在 root 运行时补 `IS_SANDBOX=1`；这不是给普通用户随便开的开关。

### 4.4 `--settings` 和 `--setting-sources`

magpie 给桥接进程的最小 settings 是：

```json
{
  "disableDeepLinkRegistration": "disable",
  "showThinkingSummaries": true
}
```

`--setting-sources ""` 让进程不读用户的 settings，避免用户的 hooks、MCP server、permissions 影响桥接。`--settings` 仍然是生效的，因为它是单独一层。

独立启动模式（模式 A 的 3.10）不需要 `--setting-sources ""`，因为用户希望保留自己的普通 Claude Code 设置；它只用 `--settings` 覆盖网关相关键。

### 4.5 stdin：怎样送一轮用户消息

Claude Code 的 stream-json 输入是逐行 JSON。最小用户消息：

```json
{"type":"user","uuid":"<uuid>","message":{"role":"user","content":[{"type":"text","text":"hello"}]}}
```

`content` 也可以是字符串。magpie 还会在内容里带上：

```text
<conversation_history>...</conversation_history>
<current_turn>...</current_turn>
<external_system_instructions>...</external_system_instructions>
[tool call ...]
[tool result ...]
```

这些不是 Anthropic 协议，而是把“调用方的会话”翻译成 Claude Code 能读的一段对话。复用进程时只送自上次 assistant 回复之后的新消息，避免重复历史。

### 4.6 stdout：stream-json 事件

逐行读取 stdout，忽略未知 `type`。主要事件：

| `type` | 用途 |
| --- | --- |
| `system`（`subtype: "init"`） | `session_id`、`model`、`tools`、`cwd` 等初始化信息 |
| `stream_event` | 增量 Anthropic 事件，常见 `message_start`、`content_block_start/delta/stop`、`message_delta`、`message_stop` |
| `assistant` | 完整的 assistant 消息；内容块含 `text`、`thinking`、`tool_use` |
| `user` | 工具结果；可能带 `tool_use_result` |
| `result` | 一轮结束；含 `subtype`、`is_error`、`result`、`usage`、`session_id` |
| `rate_limit_event` | 订阅额度/限流信息 |
| `control_response` | 对控制请求的回复 |
| `command_lifecycle` | 输入命令的生命周期 |

#### 去重

同一个 assistant 消息可能先以 `stream_event` 增量出现，再以完整 `assistant` 出现。实现时要按 message id 去重：

- 收到 `message_start` 时记录 message id。
- 后续完整 `assistant` 如果 id 已经流式输出过，只更新 usage/stop_reason，不重复发文本。
- `assistant` 的 `model` 为 `<synthetic>` 时通常是 Claude Code 自己插入的错误/提示，不要当成模型回复。

#### 结束一轮

`result` 表示这一轮 Claude Code 结束。典型处理：

- `is_error: false`：正常结束；如果还没有发过 `message_stop`，现在补上。
- `is_error: true`：用 `api_error_status` 或 502 返回错误；不要返回空回复。
- `subtype` 可以用来区分 `success`、达到最大轮数、结构化输出失败等。
- `usage` 转成 Anthropic 的 usage 字段。

#### 工具调用时不要等 `result`

当 Claude Code 决定调用 MCP 工具时：

1. 它会输出 `tool_use` 内容块。
2. MCP helper 的 `tools/call` 会阻塞，等待调用方结果。
3. 此时不会马上有最终 `result`。

网关必须在看到 `tool_use` 和对应的 `message_stop`（没有增量事件时，则是完整 `assistant` 消息的 `stop_reason: "tool_use"`）后，立刻把这一轮作为 `stop_reason: "tool_use"` 返回给调用方，并保持 Claude Code 进程存活。调用方执行工具后，用下一次 `/v1/messages` 请求带回 `tool_result`，网关再解除 MCP 阻塞，继续读取同一个进程的输出。

如果等到 `result` 才返回，调用方会永远等不到工具结果，形成死锁。

### 4.7 进程复用和会话恢复

#### 复用

一次对话的典型生命周期：

```text
请求 1：没有 run -> 启动 claude -> 发送完整历史/当前用户消息
       -> 流式返回 assistant
       -> 回复完成后，把 run 放进 idle 表
请求 2：同一个 conversation key -> 从 idle 取出 run
       -> 只发送新的用户消息
       -> 继续读同一个 stdout
```

这样做能保留 Claude Code 自己的 prompt cache，避免每轮重新写入完整历史。

建议的 key：

```text
account/config dir
  + model
  + 稳定的会话标识（客户端 session header，或历史消息哈希）
  + 工具集合
```

历史哈希要忽略：

- `cache_control`
- 请求 id、时间戳
- Claude Code 自己插入的临时提示
- 客户端可改写的通知块

如果客户端把历史改写后再发回来，哈希会变化。此时不要强行复用；要么新建 run，要么从保存的 session 切一个分支。

#### 空闲回收

magpie 默认保留空闲 run 约 1 小时，最多 6 个。实现时可以：

- 给每个 idle run 设过期时间。
- 新 run 到来时淘汰最久未用的。
- 淘汰前如果 Claude Code 支持保存 session，记录 `session_id`，以便以后 `--resume`。
- 关闭进程组、管道和临时目录。

#### 恢复

如果进程已经退出但 session 还在：

```text
claude ... --resume <session-id>
```

前提：

- `cwd` 和 `CLAUDE_CONFIG_DIR` 与之前一致，否则 Claude Code 可能找不到 session。
- 只把新消息送给它，不要把完整历史再发一遍。
- resume 失败时退回“新进程 + 完整历史”的路径。

如果不需要跨进程恢复，可以保留 `--no-session-persistence`，但进程崩溃/被回收后只能重放完整历史。

### 4.8 工具桥：MCP helper

Claude Code 不能直接执行调用方的工具，所以网关把它们暴露成一个 stdio MCP server。

#### MCP 配置

```json
{
  "mcpServers": {
    "bridge": {
      "command": "/absolute/path/to/your-helper",
      "args": [
        "claude-mcp-helper",
        "/tmp/run-123/callback",
        "/tmp/run-123/tools.json"
      ]
    }
  }
}
```

要点：

- `command` 用绝对路径；Windows 上尽量用原生 `.exe`，避免 `.cmd` 包装器。
- `callback` 文件里放完整回调 URL，权限 `0600`；token 不要放命令行。
- `tools.json` 是 MCP 工具数组：

```json
[
  {
    "name": "read_file",
    "description": "Read a file",
    "inputSchema": {
      "type": "object",
      "properties": {
        "path": { "type": "string" }
      },
      "required": ["path"]
    }
  }
]
```

#### helper 实现的方法

只需实现 Claude Code 需要的 MCP 子集：

```text
initialize
tools/list
tools/call
notifications/tools/list_changed（可选）
```

`initialize` 的 JSON-RPC result 类似：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "protocolVersion": "2025-06-18",
    "capabilities": { "tools": { "listChanged": true } },
    "serverInfo": { "name": "bridge", "version": "1" }
  }
}
```

`tools/list` 的 JSON-RPC result 返回：

```json
{
  "jsonrpc": "2.0",
  "id": 2,
  "result": { "tools": [ ... ] }
}
```

`tools/call`：

1. 从 `params._meta["claudecode/toolUseId"]` 取 Claude Code 的 tool_use id。
2. 向 callback URL POST：

```json
{
  "tool_call_id": "toolu_...",
  "name": "read_file",
  "arguments": { "path": "/tmp/a.txt" }
}
```

3. 阻塞等待网关返回：

```json
{
  "content": [
    { "type": "text", "text": "file contents" }
  ],
  "is_error": false
}
```

4. 把结果作为 MCP `tools/call` 的 result 返回给 Claude Code。

如果工具返回图片，`content` 可以用 MCP 的 image content；最小实现可以先只支持文本。

#### 网关回调

网关侧收到：

```text
POST /_your_bridge/claude-mcp/<token>
```

要做：

1. 用 token 找到活跃 run；token 过期、已结束、错误都拒绝。
2. 记录 `tool_call_id -> pending channel`。
3. 把 `tool_use` 作为 assistant 内容返回给调用方。
4. 调用方下一次请求带回 `tool_result` 时，按 `tool_use_id` 找到 pending channel，把结果送回去。
5. Claude Code 继续输出，网关继续翻译成 SSE。

第 4 步只处理这次请求里新增的 `tool_result` 和用户文字。调用方的请求通常包含整段历史，不要再把整段历史作为新消息送给 Claude Code，否则会重复工具调用或破坏 prompt cache。

安全要求：

- token 每 run 随机生成，至少 16 字节，最好 24 字节。
- token 只对活着的 run 有效；结束后立刻失效。
- 比较 token 用常量时间比较。
- 回调只监听/访问 loopback 或受控地址。
- 回调 HTTP client 不要走代理，避免被 `*_PROXY` 带走。
- 临时目录 `0700`，文件 `0600`。
- 不要在日志里打印 token、OAuth token、完整请求体。

#### 工具名

Claude Code 会把 MCP 工具名显示为：

```text
mcp__<server>__<tool>
```

工具名只保留 `[A-Za-z0-9_-]`，过长会被截断。实现时要维护“对外名字 -> 调用方原始名字”的映射，回调时还原。最简单的做法是要求调用方工具名已经符合 MCP 规则；否则用稳定哈希生成短名。

#### 工具 schema

Claude Code 可能丢弃根节点是 `oneOf`、`anyOf`、`allOf` 的 MCP 工具。调用方给的工具 schema 如果是这种形状，要在暴露前折成 `type: "object"`，合并字段和 required。

#### 工具选择

- 调用方 `tool_choice: "none"`：仍可以把工具列给 Claude Code，但模型若调用，网关直接返回错误结果，让它改用文字回答。
- 调用方指定某个工具：只提供该工具，或至少限制可调用集合。
- 多个并行工具调用：全部登记 pending；所有结果回来后再继续。
- 某个结果永远不来：设置超时，返回错误并结束 run，避免进程永久挂住。

### 4.9 新 run 的提示词怎么拼

如果没有可复用的进程，需要把调用方的完整会话翻译成 Claude Code 能读的一段消息。

建议格式：

```text
<external_system_instructions>
...调用方的 system prompt...
</external_system_instructions>

<conversation_history>
Human: ...
Assistant: ...
[tool call toolu_1 read_file {"path":"a.txt"}]
[tool result toolu_1 ...]
...
</conversation_history>

<current_turn>
Human: ...本轮用户消息...
</current_turn>
```

原则：

- 已有回复的历史和本轮消息分开，避免模型把历史图片/工具结果当成刚发生的事。
- 工具调用和结果用文本标记，但不要把它们同时再作为 MCP 调用；真实工具结果通过 MCP pending channel 送。
- system prompt 最好放在第一条用户消息里，而不是覆盖 Claude Code 自己的 system prompt。覆盖 system prompt 可能改变 Claude Code 的客户端特征和订阅判定。
- 如果一定要用 `--system-prompt-file` 或 `--append-system-prompt-file`，先用真实 Claude Code 验证行为和订阅用量。
- 图片用 base64 image content；最小版本可以先只支持文本，遇到图片明确报错。

### 4.10 `count_tokens`

Claude 订阅账号不要为了计数直接请求 Anthropic。网关应在本地估算：

```text
input_tokens = 文本字符数 / 4 + 图片固定开销 + 工具 schema 开销
```

这不是精确值，但足够 Claude Code 判断上下文进度。需要更准确时，可以使用本地 tokenizer；不要走网络。

### 4.11 错误、取消和清理

必须覆盖：

- 启动失败：返回明确错误，提示安装 Claude Code。
- 进程中途退出：返回 502，带上退出码和有界的 stderr 尾部。
- `result.is_error`：按 `api_error_status` 返回 429/4xx/5xx；没有状态时用 502。
- 调用方断开：取消等待，停止读取，关闭 stdin，杀掉进程组。
- MCP 回调超时：返回错误结果，结束 run。
- 空闲超时：关闭进程，保存 session（如支持），清理临时目录。
- 启动时清扫上次崩溃留下的临时目录和 session。
- 限制并发 run 数，避免一个网关拉起无限 Claude Code 进程。

### 4.12 登录和账号目录

最省事的做法是要求用户先用 Claude Code 正常登录：

```text
claude auth login --claudeai
```

然后网关使用该配置目录。一个账号一个目录：

```text
CLAUDE_CONFIG_DIR=<account-dir>
```

读取状态：

```text
claude auth status --json
```

magpie 不自己刷新 Claude OAuth，刷新交给 Claude Code。凭据通常在：

```text
<config-dir>/.credentials.json
```

macOS 也可能在 Keychain。不要复制、记录或回放 refresh token。

`claude setup-token` 生成的账号，只在该账号的配置目录里通过子进程环境传入：

```text
CLAUDE_CODE_OAUTH_TOKEN=<token>
```

不要放在命令行，也不要写日志。

多个进程共用同一个 `CLAUDE_CONFIG_DIR` 可能争抢凭据刷新；要么给每个账号独立目录，要么复用同一个进程。

### 4.13 交互式模式（进阶，不建议第一版做）

magpie 默认在 macOS/Linux/Windows 上使用 Claude Code 的交互式模式，而不是 `claude -p`。原因是 Anthropic 按入口计算订阅额度，`-p` 的 `sdk-cli` 入口大约消耗交互式 `cli` 入口的两倍额度，而且 `-p` 启动时没有交互式模式的 prompt cache。

实现方式是 `magpie claude-tui-helper`：

- 在 PTY/ConPTY 中启动真实交互式 Claude Code。
- 把 `-p` 参数转换成交互式参数。
- 从终端 transcript 读取输出，再翻译成 stream-json。
- 模拟用户回答终端里的信任、权限、粘贴等问题。
- 处理 Windows ConPTY、WSL、图片粘贴、终端尺寸等大量平台差异。

交互式参数大致是：

```text
去掉：-p、stream-json 格式、--include-partial-messages、
      --dangerously-skip-permissions、--no-session-persistence
加上：--permission-mode default
      --allowedTools mcp__bridge[,WebSearch]
      --no-chrome
新会话：--session-id <uuid>
```

这部分高度依赖 Claude Code 版本和终端行为。除非订阅额度成本是硬要求，否则第一版只做 `-p`，把交互式模式留作后续。

## 5. 最小复现顺序

建议按下面顺序做，每一步都能独立验收：

### 第 1 步：settings 连接器

- 找到 `$CLAUDE_CONFIG_DIR/settings.json` 或 `~/.claude/settings.json`。
- 能读、能写、能快照、能恢复。
- 不覆盖用户其它键。
- 解析失败不写入。

验收：连接 -> 断开后文件内容与连接前一致；用户中途改过的键不被覆盖。

### 第 2 步：Anthropic Messages 路由

- 实现 `POST /v1/messages`。
- 支持流式和非流式。
- 支持 `tool_use` / `tool_result`。
- 支持 `count_tokens` 本地估算。

验收：用 `curl` 或 Anthropic SDK 直接请求网关，Claude Code 能完成一次普通对话。

### 第 3 步：单轮 Claude Code 子进程

- 启动 `claude -p ...`。
- 发送一条用户消息。
- 读取 `system/init`、`stream_event`、`assistant`、`result`。
- 翻译成 Anthropic SSE。

验收：一个 HTTP 请求能拿到完整回复。

### 第 4 步：多轮进程复用

- 维护 conversation key -> idle run。
- 第二轮只发送新增消息。
- 空闲回收和进程退出处理。

验收：第二轮不会重复发送完整历史；进程复用后回复正确。

### 第 5 步：MCP 工具桥

- 写 tools.json。
- 启动 MCP helper。
- 实现 `initialize`、`tools/list`、`tools/call`。
- 回调网关并阻塞等待 tool_result。

验收：调用方发一个工具，Claude Code 产生 `tool_use`；调用方返回 `tool_result` 后，同一个进程继续回答。

### 第 6 步：会话恢复

- 记录 `system/init` 的 `session_id`。
- 进程退出后尝试 `--resume <id>`。
- resume 失败退回完整历史。

验收：杀掉进程后，同一个对话还能继续。

### 第 7 步：边界和清理

- 取消、超时、进程崩溃、并发限制。
- 临时目录、token、session 清理。
- stderr、日志脱敏。

验收：上述每条都有测试。

### 第 8 步：交互式模式（可选）

只在确实需要降低订阅额度成本时做。先确保 `-p` 版本稳定。

## 6. 测试计划

### 6.1 测试环境

- 使用临时 HOME 和临时 `CLAUDE_CONFIG_DIR`。
- 不读写真实 `~/.claude`、`~/.claude.json`、`~/.config/magpie`。
- 把临时目录放在测试自己的 `t.TempDir()` / `tmpdir` 下。
- 测试结束等待所有子进程退出。

### 6.2 fake `claude`

写一个可执行的 fake CLI，行为：

1. 记录 argv 和关键 env 到文件。
2. 校验是否包含预期参数。
3. 从 stdin 读取用户消息。
4. 向 stdout 输出：

```json
{"type":"system","subtype":"init","session_id":"test-session","model":"test-model"}
{"type":"stream_event","event":{"type":"message_start","message":{"id":"msg_1","model":"test-model","usage":{"input_tokens":1,"output_tokens":0}}}}
{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}}
{"type":"stream_event","event":{"type":"content_block_stop","index":0}}
{"type":"stream_event","event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}}
{"type":"stream_event","event":{"type":"message_stop"}}
{"type":"result","subtype":"success","is_error":false,"result":"hello","session_id":"test-session"}
```

工具调用场景让 fake 输出 `tool_use`，然后等待 MCP callback 或模拟阻塞。

### 6.3 必测用例

**settings 连接/断开**

- 键原来不存在 -> 连接 -> 断开 -> 键不存在。
- 键原来有值 -> 连接 -> 断开 -> 原值恢复。
- 连接后用户改了值 -> 断开不覆盖用户的新值。
- settings 不是合法 JSON -> 不写。
- 云厂商开关在 settings / `.claude.json` / shell -> 能检测并拒绝声称已走网关。
- `ANTHROPIC_MODEL` 在连接时被删除，断开时按快照恢复。
- `[1m]` 模型写入和网关解析。

**Claude Code 客户端路由**

- 流式文本。
- 非流式文本。
- `tool_use` 和 `tool_result`。
- 多个并行工具调用。
- `count_tokens`。
- 错误状态和 SSE error。

**Claude 订阅桥**

- 子进程启动参数正确。
- token 不在 argv，只在 `0600` 文件。
- 第一个 turn 能收到完整回复。
- 第二个 turn 复用同一个进程，只发送新增消息。
- 工具调用闭环。
- 工具结果超时。
- 进程崩溃返回 502。
- 取消时进程组被杀掉。
- `--resume` 成功和失败回退。
- idle 回收和最多并发数。

**MCP helper**

- `initialize` 返回协议版本和 capabilities。
- `tools/list` 返回工具。
- `tools/call` 带 `_meta` toolUseId 时回调正确。
- 回调 token 错误/过期/已结束 -> 拒绝。
- 回调 client 不走代理。
- 工具名截断/哈希后能映射回原名。

### 6.4 真实 smoke test

不要放进默认 CI。单独用环境变量开启，例如：

```text
REAL_CLAUDE_SMOKE=1
```

用一个专门的测试账号目录，不碰用户主账号：

- 普通文本一轮。
- 两轮对话。
- 一个只读工具。
- 取消一次。

记录跑了什么、Claude Code 版本、平台。没有真实跑过的，不要对外说“已支持”。

## 7. 常见坑

| 坑 | 表现 | 处理 |
| --- | --- | --- |
| `ANTHROPIC_BASE_URL` 写成 `/v1` | 请求变成 `/v1/v1/messages` | 根地址只写到端口 |
| 写了 `ANTHROPIC_MODEL` | `/model` 选择不生效 | 删除，主模型写顶层 `model` |
| 云厂商开关为真 | 请求绕过网关去 Bedrock/Vertex | 检测并清除/报告 |
| `ANTHROPIC_API_KEY` 和 `AUTH_TOKEN` 同时存在 | 凭据冲突或多余 x-api-key | 独立启动文件里把 API key 置空 |
| `--settings` 当成完全隔离 | 用户 hooks/MCP/permissions 仍生效 | 加 `--setting-sources ""`，并注意 managed settings |
| 无条件 `--dangerously-skip-permissions` | 本机任意工具执行风险 | 只在严格受限、仅 MCP 工具时使用 |
| root 下跳过权限失败 | `cannot be used with root/sudo privileges` | 沙箱内才设 `IS_SANDBOX=1` |
| Windows 直接 spawn `claude.cmd` | 参数/引号错误 | 通过 `cmd.exe` 或原生 `.exe` |
| SOCKS 代理 | `UnsupportedProxyProtocol` | 本地 HTTP 代理桥 |
| `[1m]` 直接转发上游 | 模型 404 | 网关解析前去掉 |
| 把网关模型 id 传给 Claude Code | Claude Code 不认识/上下文错误 | 映射成 Anthropic 模型 id |
| 工具调用等 `result` 才返回 | 调用方和 Claude Code 互相等待 | 看到 tool_use/message_stop 就返回 |
| `tool_result` 当普通文本再喂回去 | 工具被重复调用/死锁 | 通过 MCP pending channel 送回 |
| 不读 stderr | 子进程管道写满，卡死 | 一直排空 stderr，保留有界尾部 |
| 读取超长 stdout 行 | scanner 默认 64KB 限制 | 提高 buffer，至少几 MB |
| 只杀父进程 | 子进程/工具进程残留 | 杀进程组 |
| 多进程共用同一配置目录 | OAuth 刷新竞争 | 独立 `CLAUDE_CONFIG_DIR` 或复用进程 |
| `/model` 写回用户 settings | 普通 `claude` 请求到网关模型 | 运行后检测并恢复/迁移 |
| session resume 的 cwd 不一致 | 找不到 session | 固定 cwd，或按 cwd 记录 session |
| 日志打印完整请求/响应 | token、用户内容泄露 | 结构化脱敏，只记元数据 |

## 8. 源码索引

以下路径相对于本仓库根目录。函数名比行号稳定，优先按符号查找。

### Claude Code 作为客户端

| 内容 | 位置 |
| --- | --- |
| Claude Code agent 定义、settings 写入、tier、Unwire | [`internal/agent/claude.go`](../internal/agent/claude.go)：`claudeIn`、`writeTiers`、`Unwire` |
| 独立 launch settings | [`internal/agent/claude_launch.go`](../internal/agent/claude_launch.go)：`ClaudeLaunch`、`ClaudeLaunchBefore`、`ClaudeLaunchAfter` |
| `magpie claude run` | [`internal/cli/claude_launch_cli.go`](../internal/cli/claude_launch_cli.go)：`claudeRun` |
| Claude 配置目录 | [`internal/appdir/claude.go`](../internal/appdir/claude.go)：`ClaudeDir` |
| 路由入口 | [`internal/gateway/gateway.go`](../internal/gateway/gateway.go)：`/v1/messages`、`/messages`、`/v1/messages/count_tokens` |
| 计数 | [`internal/gateway/gateway.go`](../internal/gateway/gateway.go)：`countTokens`、`countOn` |

### Claude 订阅桥

| 内容 | 位置 |
| --- | --- |
| 订阅桥入口和生命周期 | [`internal/gateway/claude_subscription.go`](../internal/gateway/claude_subscription.go)：`serveClaudeSubscription`、`serveSubscription` |
| 启动 Claude Code | 同上：`startRun`、`claudeCLIArgs` |
| stream-json 解析 | 同上：`readOutput` |
| 工具回调 | 同上：`mcpCall` |
| 提示词渲染 | 同上：`renderClaudePrompt`、`renderClaudeTurn` |
| run 复用/空闲 | 同上：`resume`、`keepIdle`、`idleLongest`、`idleMost` |
| MCP helper | [`internal/claudebridge/mcp.go`](../internal/claudebridge/mcp.go)：`RunMCP` |
| 交互式参数转换 | [`internal/claudetui/tui.go`](../internal/claudetui/tui.go)：`interactiveArgs` |
| Claude 登录/配置目录 | [`internal/provider/claude_signin.go`](../internal/provider/claude_signin.go)、[`internal/provider/claude_dirs.go`](../internal/provider/claude_dirs.go) |
| Claude Code 发现/下载 | [`internal/claudecode/claudecode.go`](../internal/claudecode/claudecode.go)：`Find`、`Latest`、`Install` |

### 设计参考

- [Claude subscription bridge](subsystems/claude-subscription-bridge.md)
- [Agent wiring](subsystems/agent-wiring.md)
- [Integrating your app or agent with magpie](integrating.md)
- [Code standards](code-standards.md)

### 可参考的测试

- [`internal/agent/claude_launch_test.go`](../internal/agent/claude_launch_test.go)
- [`internal/agent/claude_login_test.go`](../internal/agent/claude_login_test.go)
- [`internal/agent/claude_cloud_test.go`](../internal/agent/claude_cloud_test.go)
- [`internal/cli/claude_launch_cli_test.go`](../internal/cli/claude_launch_cli_test.go)
- `internal/gateway/claude_*_test.go`（`claude_subscription.go` 的行为测试，目录见 [`internal/gateway`](../internal/gateway)）
- [`internal/gateway/claude_callback_addr_test.go`](../internal/gateway/claude_callback_addr_test.go)
- [`internal/gateway/claude_idle_test.go`](../internal/gateway/claude_idle_test.go)
- [`internal/gateway/claude_prompt_history_test.go`](../internal/gateway/claude_prompt_history_test.go)
- [`internal/claudebridge/mcp_test.go`](../internal/claudebridge/mcp_test.go)

## 9. 不要复刻的部分

下面这些是 magpie 产品层的东西，不是接入 Claude Code 的必要条件：

- GUI/Agents 页面、图标、i18n
- WSL twin、Claude Desktop、Claude Magpie
- provider/plugin 迁移系统
- usage ledger、routing group、模型目录、价格和配额
- 自动更新、遥测、通知
- 交互式 TUI 的粘贴、图片、终端尺寸、transcript 解析
- 会话分支、quota shelf、refusal fallback、自动重试

先把“settings 连接 + Anthropic Messages + 单进程 + MCP 工具闭环”做稳，再考虑这些。

