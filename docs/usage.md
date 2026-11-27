# 用量

本文写这个进程现在的用量模块：它记什么、怎么读、哪里停下。字段与失败语义以 [state](subsystems/state.md)、[gateway](subsystems/gateway.md)、[console](subsystems/console.md) 为准；行为合同是 [用量账本](design/usage-ledger.md)，实现顺序是 [把用量模块补到接近 magpie](design/usage-parity.md)。

它回答「这次调用发生了什么」：哪个 agent、发给哪家、用了哪个模型和密钥、花了多少 token、读取时值多少钱、是否失败或取消。路由不从这份账选路，余额和订阅额度也不从这份账扣减。

| 做什么 | 哪里 |
| --- | --- |
| 一行记录、追加、分块读取、汇总 | [state.AttemptRecord](../internal/state/usage.go)、`AppendUsage`、`ReadUsage`、`Summarize`、[usage_index.go](../internal/state/usage_index.go) |
| 窗口与「清除」判定 | [state.TokenLimit.WindowStart](../internal/state/limits.go)、`UsageHide`、`Config.HiddenOnUsagePage` |
| 对话与订阅尝试写入 | [gateway.Server.note](../internal/gateway/chain.go)、[login.go](../internal/gateway/login.go) |
| 补报外部调用 | [gateway.Server.reportUsage](../internal/gateway/reported.go) 的 `POST /v1/magpie/usage/requests` |
| 拨号前检查上限 | [gateway.Server.spendBlock](../internal/gateway/spend.go) |
| 读取时计价 | [price.Engine](../internal/price/price.go) |
| 控制台读取 | [console.Server.listUsage](../internal/console/server.go) 及其相邻的 `usage*`、`clearUsage`、`getPricing` |
| 用量页 | [web/src/sections/Usage.jsx](../web/src/sections/Usage.jsx) |
| 厂商密钥余额 | [balance.ForProviders](../internal/balance/balance.go) |
| 订阅额度 | [login.Usage](../internal/login/usage.go) |

## 账记在哪

用量是配置旁边的第二份存储，文件是 `usage/attempts.jsonl`。默认在用户配置目录的 `aigate/usage/`；设置了 `AIGATE_CONFIG` 时，账在那个配置文件所在目录的 `usage/`，默认位置不再读写。目录权限 `0700`，文件权限 `0600`（平台承认这些位时）。文件不加密。只追加，不改旧行。

带会话标识且回复完整结束的调用，另写在同一目录的 `conversations/<会话标识的 SHA-256>.jsonl`。那是会话记录，不进用量合计。用量行里不存提示词和回复正文。

写入按行加锁。追加前，`started` 或 `finished` 为零会填当前 UTC 时间；已经写出的 `ms` 保持不变。模型回复已经交给调用方之后，追加失败不再改变回复，也不改变 HTTP 状态。

读取把已解析的行收成每块 512 行，满块冻结。文件被替换或变短时整份重读；只在末尾变长时从上次的块尾之后读新行。末尾没有换行的半行在读取时跳过，不使文件变成不可读。下一次追加前先把这段残片截掉，再从干净的换行后写新行；完整行从不会被改动。这个版本没有 `log-v1.bin` 磁盘缓存，进程启动后的第一次读取会解析整份文件，但结果与逐行重读一致。文件不存在是空列表，状态仍是 `ok`。读失败，或某一整行解析不了，返回 `{"status":"unknown"}`，不返回半份合计、余额或额度。

## 一条记录

JSON 名是文件格式，旧名不能改。没有值的字段省略。

| 字段 | JSON | 含义 |
| --- | --- | --- |
| 时间 | `started` `finished` `ms` | 调用开始和结束。最终行从请求进网关算起；`ms` 是整次请求的耗时。两个时间在追加时为零会补成当前时刻；`ms` 已给则保留。 |
| 同一次请求 | `routeId` | 一次用户请求的全部尝试共用；补报行用调用方给的请求 id。 |
| 配置路由 | `route` | 配置里的路由 id，可为空。 |
| 供应商与模型 | `providerId` `model` | 实际尝试的供应商 id 和发给它的模型 id。 |
| 结果 | `outcome` `status` `rejected` | 结果分类、上游 HTTP 状态、是否本地拒绝。 |
| 错误 | `err` `errType` | 给调用方看的原因和供应商错误类型。 |
| token | `in` `out` `cacheRead` `cacheWrite` `cacheWrite1h` `reasoning` | 拆分后的计数；`in` 不含缓存读写；`cacheWrite1h` 是缓存写里按一小时计费的部分。 |
| 原始用量 | `usage` | 供应商自己的用量对象。缺了就是不知道，不写成零。 |
| 会话 | `session` | 请求头里的会话 id。没有就不写，不编造。 |
| 尝试 | `tries` `handedOn` | 最终行的 `tries` 大于 1 表示前面还有失败尝试；`handedOn` 表示这一行是已计费、后面还有结局的失败尝试。 |
| 调用方 | `caller` `agent` `protocol` | 调用方名字、agent id、入站协议。 |
| 密钥 | `tokenId` `callerKeyId` `callerIssuerId` `providerKeyId` | 旧 `tokenId` 与 `callerKeyId` 同义；后两个是签发密钥和上游密钥的指纹。都不是密钥原文。 |
| 账号 | `providerAccount` | 订阅账号的登录名或邮箱。 |
| 来源 | `source` | 空是网关经手的调用；其余见下表。 |
| 自报价 | `cost` | 外部上报的美元费用。有它时不再乘标价。 |
| 耗时细节 | `ttft_ms` `first_text_ms` `queue_ms` `retry_ms` | 首 token、首段正文、排队、重试的毫秒。非流式回复的首 token 为 0。 |
| 字节 | `bytes_in` `bytes_out` | 发给上游的请求体字节和写给调用方的回复体字节。 |
| 操作 | `operation` | `generate_content`、`native` 等非对话调用的区分。 |

token 计数来自供应商回复的用量对象；OpenAI 与 Gemini 的 prompt 合计含缓存，写入时减掉缓存读；Anthropic 的 `input_tokens` 本身就是未缓存输入。Anthropic 的 `cache_creation.ephemeral_1h_input_tokens` 写进 `cacheWrite1h`，并封顶在 `cacheWrite` 以内。只有总数、拆不开的对象不写这些字段。旧行缺少新字段时按旧规则解析。

`source` 决定这一行进不进汇总和限额：

| 值 | 谁写入 | 用量合计 | 密钥限额 |
| --- | --- | --- | --- |
| 空 | 网关经手的调用 | 计入 | 计入 |
| `reported` | 客户端补报自己直连厂商的调用 | 计入 | 不计入 |
| `probe` | 探测 | 不计入 | 不计入 |
| `mcp` | MCP 工具调用 | 不计入 | 不计入 |
| `search` | 搜索 API | 不计入 | 不计入 |

`reported` 行照进总合计、token 和费用，但不进会话分组；用量页之外的读取器也不读它们。

## 一次请求写成几行

普通对话在候选循环结束时写**一行最终记录**。字段来自这次请求和最后一次实际发出的尝试：

- `started` 是请求进网关的时间，`ms` 是整次请求的耗时。
- `model` 是最后一次尝试的模型，`session` 是请求头里的会话。
- 最终行算一次调用；前面失败过时写 `tries`。
- `routeId` 由每次用户请求新生成，所有尝试共用。

已经向厂商拨号、产生了 token、然后失败并换下一家的尝试，另写一行 `handedOn: true`。这一行的 `started`/`finished`/`ms` 是这次尝试自己的。它的 token 和费用照加，但不增加调用次数或失败次数。没有产生 token 的失败不另写行，只留在最后一次尝试或最终行里。

失败但已经是最后一个候选时不写 `handedOn`，直接成为最终行；否则 token 会重复计算。

取消：状态 499 且错误文案是 `the agent canceled the request`（或 `errType` 为 `agent_canceled`）算取消，不算失败，也不算回答。网关自己等超时记的 499 仍是失败。取消行不占密钥限额。

本地拒绝：`rejected: true`，或 `providerId` 为空且 `status >= 400`，不算成功调用。

## 汇总与分组

`Summarize` 只读通过筛选的行：

- `handedOn` 行不加 `calls`、`failed`、`canceled` 或 `retried`，但它的 token、字节和费用照加。
- 最终行算一次调用。`status >= 400`、有 `err`、或 `rejected` 且不是取消，算失败。
- 取消只加 `canceled`。
- `tries > 1` 且最终被回答，算一次调用并加 `retried`。
- `probe`、`mcp`、`search` 在进入合计前丢掉。

`Summary` 有一份总合计和六组分组：`agents`、`models`、`providerKeys`、`accounts`、`callerKeys`、`sessions`。每组有调用次数、五种 token、费用和无标价次数。没有 id 的行不进那一组；会话组只包含写了 `session` 且不是 `reported` 的行。

窗口按读者本地日历：`today` 是今天 0 点，`7d` 是含今天共 7 天，`30d` 是含今天共 30 天，`all` 是零时间起。`series` 在 `today` 按小时、`7d` 和 `30d` 按天、`all` 按周，每个点是该桶的合计。0 点前的调用不进 `today`。

请求列表按 `started` 新的在前，支持 `offset`、`limit` 和筛选 `agent`、`model`、`callerKeyId`（旧名 `tokenId`）、`providerId`、`routeId`、`source`、`start`、`end`、`period`。

## 读取时计价

费用不写回文件，每次读取按下面的顺序取价：

1. 用户价：`供应商/模型`、`供应商/*`、所有供应商下的该模型、所有供应商的所有模型。写了 0 也算，不会退回目录价。
2. 目录价：models.dev 里这家供应商的标价；供应商 id 不是目录名时，再试显示名、基地址主机名，最后试纯模型 id。纯模型 id 只在目录把该模型归到模型厂商时使用；只有转售商报价时按无标价处理，不拿转售商价格猜厂商价。
3. 供应商倍率，或该上游密钥自己的倍率；乘在目录价上。用户写的价不再乘这一层。
4. 行上的 `started` 落在高峰或费率窗口内时，再乘窗口系数。

费用公式（单价单位是美元 / 百万 token）：

```text
cacheWrite1h = clamp(cacheWrite1h, 0, cacheWrite)
cost = (in * input
      + out * output
      + cacheRead * cacheRead
      + (cacheWrite - cacheWrite1h) * cacheWrite
      + cacheWrite1h * oneHour) / 1e6
```

缓存单价缺了就不计这部分缓存 token，**不回退到输入价**。`in + out == 0` 的行不计费，也不算无标价；自带 `cost` 的外部补报行例外，按那个数计费。有 token 但没有任何价，费用不加，`unpriced` 加 1。外部上报带 `cost` 时用那个数，不再乘标价和倍率。

## 网关密钥上限

一把网关密钥最多五条 `limits`。每条可以卡美元、卡 token，或两个都卡；`0` 或省略表示这项不封顶。窗口用 `window`：

- `today`：本地今天 0 点起。
- `week`：本机时区从周一起。
- `month`：从 1 日起。
- `all`：全部。
- `hours` / `days`：从当前时刻往回滚动；给了 `since` 就固定从那个时刻起。

`provider` 和 `model` 可以把计数收窄到某些供应商/模型。旧字段 `spendLimit` 加 `spendPeriod` 仍作为一条美元上限读取；旧 `spendLimit` 为 0 或省略表示不封顶。

拨号前，`spendBlock` 读该密钥在每条窗口里的行：

- token 用 `in + out + cacheRead + cacheWrite`。
- 美元用上面的读取时计价。
- `handedOn` 的 token 和费用占上限；补报、探针、MCP、搜索、取消和本地拒绝不占。
- 短时密钥的花费同时算进签发它的密钥：`callerIssuerId` 指向签发者时，签发者自己的上限也检查，并且它签发的其它短时密钥的花费一起加总。

任何一条到顶返回 429 `spend_limit`，不再拨号。价目或用量文件读不出来、或有账单 token 却没有价，返回 503 `price_unknown`，不放行。

## 补报

`POST /v1/magpie/usage/requests` 让客户端把直连厂商的调用补进账本。请求体字段：

```json
{
  "requestId": "客户端自己的 id",
  "at": "2026-01-01T00:00:00Z",
  "agent": "codex",
  "provider": "provider-id",
  "providerAccount": "user@example.com",
  "model": "model-id",
  "session": "session-id",
  "status": 200,
  "ms": 1200,
  "in": 1000,
  "out": 200,
  "cache_read": 0,
  "cache_write": 0,
  "cache_write_1h": 0,
  "reasoning": 0,
  "cost": 0.0042,
  "err": "",
  "err_type": "",
  "usage": {}
}
```

`requestId`（或 `id`）必填；同一 id 第二次返回 `duplicate: true`，不重复写。已由网关自己写过同一 id 时也一样。`source` 强制为 `reported`，不进密钥限额。`at` 缺省为当前时刻，`ms` 只用于算 `finished`。也可以只给 `usage` 对象，让网关按供应商格式拆 token。

## 页面与 API

控制台需要控制台会话。用量相关的读法：

| 方法 | 路径 | 读什么 |
| --- | --- | --- |
| GET | `/usage?period=&agent=&model=&callerKeyId=&providerId=&routeId=&source=&start=&end=&offset=&limit=` | 合计、分组、序列、分页记录、余额、订阅额度、读者时区 |
| GET | `/usage/heatmap?…` | 最近 53 周，每天一格；使用同一套筛选 |
| GET | `/usage/requests.csv?…` | 同一批行，含 token、读取时费用、状态、会话、密钥 id |
| GET | `/usage/traffic` | 进程内存里的进行中请求和累计上下行字节，不读历史文件 |
| GET | `/usage/requests/content?session=&from=&to=` | 该会话归档的对话轮次；用量行本身没有正文 |
| POST | `/usage/clear` | 写一条「这个供应商的这个模型，这个时刻之前不显示」 |
| GET / PUT | `/pricing` | 用户价 `prices` 和高峰 `peaks` |

用量页按上述筛选展示合计、分组、序列、行列表和热力图，提供 CSV 下载、分页、清除，以及按会话打开对话原文。CSV 的费用是读取时算的。`GET /usage` 的响应带 `timezone`，是本机时钟的时区名，用来标每天和每小时。

密钥页：`GET /tokens` 返回 `limits`、旧 `spendLimit`/`spendPeriod` 和 `issuerId`；`POST /tokens` 创建并返回保存后的模型表；`PUT /tokens/{id}` 改模型表、限额和吊销状态。

供应商页：`multiplier` 是供应商倍率，`keyMultipliers` 按密钥顺序覆盖它；添加密钥时非空倍率表会补上供应商倍率，删除密钥时同步删掉对应项。`balanceUrl`、`balanceField`、`balanceManual`、`balanceCurrency` 给余额卡片用。

「清除」只影响用量页。`usage/attempts.jsonl` 不删，密钥上限和配额估算仍然数这些行。

## 余额与订阅额度

`balances` 和 `quotas` 跟用量页一起当场现查，不从账本减：

- 余额每个上游密钥一行。内置 `api.deepseek.com` 的 `/user/balance` 和 `openrouter.ai` 的 `/api/v1/credits`（额度减已用）。内置表不认识的 host 标 `unsupported`，不会去拨。
- 用户可以在供应商上填 `balanceUrl` 和 `balanceField`（点分 JSON 路径）；也可以填 `balanceManual` 和 `balanceCurrency`。接口从未成功且没有上一次读数时显示手填值；有上一次成功读数时优先保留 `stale` 值。
- 同一基地址和同一密钥的结果缓存一分钟。刷新失败时保留上一次成功读数，标 `stale` 和 `error`，不把卡片清成零。
- 普通 OpenAI、Anthropic、Gemini API 密钥没有余额接口，不会去猜。
- 订阅额度仍是 Codex 的 `/wham/usage`、Claude 本机 `/usage`、Gemini 的 `quotaInfo` 三条读取路径；被插件占住的账号不出现。查不到是 `unknown`，窗口留空。

## 边界

- 路由策略 `usage` 继续用内存里的使用量，不回放 `attempts.jsonl`；发送量和回复速度也是内存里的衰减平均。用量行上的 `ttft_ms` 只用于展示和导出，不驱动选路。
- 供应商 id 不变；控制台改的是显示名。旧行保留写入时的 id，因此不需要改名映射。以后若允许改 id，才需要「按调用时间映射」。
- 没有 `log-v1.bin` 磁盘缓存，启动后第一次读取解析整份文件。没有 OTLP 导出；用量行只记元数据，不记提示词和回复。
- 没有缓存命中率和生成速度的汇总字段；这些只在路由内存里计算。
- 目录价只有一组单价，不按输入规模选档。
- 旧行不迁移。缺少的字段按旧规则解析，标价按当前配置重算，除非该行自带 `cost`。

## 相关说明

| 内容 | 文档 |
| --- | --- |
| 记录格式、窗口、读失败 | [State](subsystems/state.md) |
| 何时写入、密钥上限、补报 | [Gateway](subsystems/gateway.md) |
| 控制台接口与页面 | [Console](subsystems/console.md) |
| 行为合同 | [用量账本](design/usage-ledger.md) |
| 实现顺序 | [把用量模块补到接近 magpie](design/usage-parity.md) |
