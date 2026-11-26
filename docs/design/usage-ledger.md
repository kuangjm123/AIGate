# 用量账本

用量模块记的是网关经手的每一次调用：谁发起、发给哪家、花了多少 token、多少钱、是否失败。页面、命令行、密钥限额和配额估算都读这份账，不各自再记一份。

本文是可以照着实现的设计。实现出来的核心必须满足下面的数据模型、写入规则、计价公式和汇总规则。厂商配额卡片怎么向各家拉数、每个 agent 的会话文件怎么解析，是接在这份账上的读取器，不在本文里逐家展开。会话文件和账本怎么合成一条会话，见 [Gateway Session Aggregation](subsystems/gateway-sessions.md)。

源码以这些符号为准。行为变了先改符号，再改本文。

| 做什么 | 哪里 |
| --- | --- |
| 一条调用 `Record`、追加、汇总 | `internal/usage/usage.go` |
| 失败、取消、回答的判定 | `internal/usage/ledger.go` |
| 探针行不进汇总 | `internal/usage/probes.go` 的 `IsProbe` |
| 磁盘上的块索引 | `internal/usage/log_index.go`、`internal/usage/log_disk.go` |
| 标价 | `catalog.Price.CostSplit`、`catalog.Price.In` |
| 网关把一次对话写成行 | `internal/gateway/gateway.go` 里候选循环结束处，以及失败且已计费时的 `failedRow` |
| 补上调用方密钥再落盘 | `appendUsage`（`internal/gateway/lan.go`）→ `budget.Append` |
| 页面读账 | `usageRoutes`（`internal/gui/usage.go`） |

## 边界

账本只回答「这次调用发生了什么」。它不做这些事：

- 不决定下一次请求发给谁。路由读的是自己的 trace 和配额，不回放账本来选路。
- 不保存提示词和回复正文。`BodyIn`、`BodyOut`、`Local`、`Watched` 不写入文件。正文在请求档案里，行上只留 `Archive` 这种 `"<date>/<id>"` 指针。
- 不把客户端 IP 同步到别的机器，也不放进 `GET /v1/magpie/usage/requests`。
- 记账失败不得让这次调用失败。`Append` 写不进去就返回，调用方已经把回复交给 agent。

插件 `plugins/usage` 只是用量页的开关。关掉它，页面从窗口消失；网关照常 `Append`，托盘上的额度卡片照常显示。

## 存储

账本是配置目录里的一个 JSON Lines 文件，和 `providers.json` 同级：

```text
~/.config/magpie/usage.jsonl
```

路径是 `filepath.Join(filepath.Dir(provider.Path()), "usage.jsonl")`。每一行是一条 `Record` 的 JSON，以换行结束。文件权限 `0600`。只追加，不改旧行。供应商改名之后，旧行仍写着当时的 provider id；读的时候用 `provider.Renames` 按调用时间映到现在的 id，并记住原来的 id 用来计价（`renameBy`、`priceID`）。

写入用一把互斥锁。非 Windows 上把文件保持打开，避免每次调用都 open/close。打开期间如果文件被换掉（设备号或 inode 变了），关掉重开。Windows 每次写完就关，因为打开的文件不能被删或改名。目录不存在时才创建。

读不能每次把整个文件解析进内存。生产实现把已解析的行收成块，每块 `512` 行（`logBlockRows`）。满块冻结。进程启动时，若磁盘缓存 `log-v1.bin`（魔数 `magpie-log-v2\n`，放在会话缓存目录旁）所覆盖的前缀 SHA-256 仍与 `usage.jsonl` 的前缀一致，就直接用这些块，只解析前缀之后的新行。缓存至少隔 10 分钟才重写。缓存对不上就整份重解析，结果必须和逐行 `json.Unmarshal` 相同。

`Load(since)` 是给测试和简单调用方的全量扫描：跳过探针行，丢掉 `since` 之前的行，半行（没有换行）留给下次。页面走的是块索引上的 `Summarize`，不是 `Load`。

## 一条记录

时间用 `t`。token 用 `in`、`out`、`cache_read`、`cache_write`。缓存读写都不计入 `in`。`cache_read` 和 `cache_write` 每次都写，包括 0，这样「缓存没命中」和「这条旧记录根本没这个字段」可以分开。没有这两个字段的旧行，读出来是 0，当作没命中。

实现时至少要有这些字段。JSON 名是文件格式，不能改。

| 字段 | JSON | 含义 |
| --- | --- | --- |
| 时间 | `t` | 调用开始。零值在 `Append` 里改成 `Clock()` |
| 调用方 | `agent` | magpie 的 agent id，或客户端产品名 |
| 供应商 | `provider` | 实际应答的供应商 id。本地拒绝、还没选上游时为空 |
| 模型 | `model` | 发给这家的模型 id |
| 用户点的名字 | `req` | agent 请求里的模型名，可以是路由组或别名 |
| 上游自称 | `served` | 回复里供应商说自己用的模型，没有就不写 |
| 再后面的一家 | `upstream` | 聚合商（如 OpenRouter）说的实际厂商 |
| token | `in` `out` `cache_read` `cache_write` | 见上。`cache_write_1h` 是其中按 1 小时缓存计费的部分，其余按 5 分钟 |
| 推理 token | `reasoning` | 输出里属于推理的数量，没有则为 0 |
| 推理档位 | `effort` | 这次要求的档位 |
| 状态 | `status` | HTTP 状态。agent 自己取消用 `499` |
| 耗时 | `ms` | 从请求进网关到这次尝试结束 |
| 首 token | `ttft_ms` `first_text_ms` | 到第一段内容、到第一段正文。非流式为 0 |
| 错误 | `err` `err_type` | 给 agent 看的原因，和供应商错误类型 |
| 会话 | `session` | `X-Magpie-Session` 或该 agent 自己的会话头。没有会话 id 的请求不编造一个 |
| 同一次路由 | `route_id` | 一次用户请求的全部尝试共用 |
| 尝试次数 | `tries` | 大于 1 才写。这是最终那一行，表示前面有失败的尝试 |
| 已移交 | `handed_on` | 这一行是失败且已经被计费的尝试，后面还有一行才是这次请求的结局 |
| 密钥 | `providerKeyId` `callerKeyId` `callerIssuerId` | 上游密钥指纹、调用方网关密钥、签发这条短时密钥的密钥。不写密钥原文 |
| 账号 | `providerAccount` | 订阅账号的邮箱或登录名，不是 token |
| 来源 | `source` | 空是网关自己的调用。其余见下表 |
| 自报价 | `cost` | 仅外部上报的调用。有则计价不再用标价 |
| 套餐积分 | `credits` | 供应商在回复里说的本次积分，没有则省略 |

`source` 只有这几种，决定这一行进不进汇总：

| 值 | 谁写入 | 汇总 |
| --- | --- | --- |
| 空 | 网关经手的调用 | 计入 |
| `reported` | 客户端通过 `POST /v1/magpie/usage/requests` 补报它自己直连厂商的调用 | 只进用量页。密钥限额、配额估算、会话归并、路由都不读它 |
| `probe` | magpie 自己的探测 | 不进任何汇总 |
| `mcp` | MCP 工具调用 | 不进汇总，请求列表单独可见 |
| `search` | 搜索 API 的调用 | 不进汇总 |

本地拒绝：`rejected: true`，或者 `provider` 为空且 `status >= 400`。这种行不算一次成功调用。

取消：`status == 499` 且 `err` 为 `the agent canceled the request`（或 `err_type` 为 `agent_canceled`）。取消不是失败，也不是成功。magpie 自己等超时而记的 499 仍然是失败，因为错误文案不是上面那句。

## 一次对话写成几行

普通对话在候选循环结束时写最终一行。字段从这次请求和最后一次实际发出的尝试来：

- `t` 是请求进入网关的时间，`ms` 是整次请求的耗时。
- `model` 是最后一次尝试发给上游的模型，`req` 是 agent 原来点的名字。
- token 用这次回复里结算过的数（`Usage.billed()`）：输入、输出、缓存读、缓存写、1 小时缓存写、推理。
- `route_id` 是这条路由的 id。路由上的尝试多于一次时，最终行写 `tries`。
- `session` 用请求头里的会话。`appendUsage` 若发现还是空的，再补一次。同时写上调用方网关密钥和是否来自本机。

失败但厂商已经计费的尝试另写一行，不等到最终行。条件是这次尝试已经知道协议（`call.To` 非空），并且 `in + out + cache_read + cache_write > 0`。这一行 `handed_on: true`，`t` 和 `ms` 是这一次尝试自己的，不是整次请求的。没产生 token 的失败不写行，只留在路由 trace 里。

所以同一次请求在文件里是：零行或多行 `handed_on`，然后至多一行最终行。它们的 `route_id` 相同。

汇总时（`Totals.count`）：

- `handed_on` 的行不增加调用次数、失败次数、取消次数。它的 token、字节和 `credits` 仍然加进合计，因为厂商已经扣了。
- 最终行算一次调用。`status >= 400` 或有 `err`，且不是取消，算失败。取消算取消。`tries > 1` 且最终被回答了，算「重试后成功」，调用次数只加 1。
- 探针行在进入汇总之前丢掉。

绘图、嵌入、视频、原生订阅桥接走各自的 `appendUsage`，但行的形状相同。`operation` 用来区分 `generate_content`、`native` 这类不是对话的调用。

## 计价

标价单位是美元 / 百万 token。一次调用的费用：

```text
cacheWrite1h = clamp(cacheWrite1h, 0, cacheWrite)
tier = price.At(in + cache_read + cache_write)   // 按输入规模选档
price = price.In(t)                               // 高峰时段先乘高峰系数，再清掉高峰标记
cost = (in * input
      + out * output
      + cache_read * cacheRead
      + (cache_write - cacheWrite1h) * cacheWrite
      + cacheWrite1h * oneHour) / 1e6
```

`Price.In` 只在这次调用的时间落在该价的高峰窗口内时，把各项单价乘上高峰系数。窗口用调用自己的时间，按本机时区解释，不用「现在」。

外部上报若带了 `cost`，用那个数，不再乘标价。没有 token（`in + out == 0`）的行不计费，也不算进「无标价」的次数。有 token 但找不到价，费用不加，`unpriced` 加 1。

用户在设置里给模型或密钥写的倍率，叠在标价上，由读账时的 `Pricer` 选价，不写回 `usage.jsonl`。标价改了，历史费用按新价重算，除非该行有自己的 `cost`。

## 汇总

四个窗口，边界按读者所在时区的本地日历日：

| period | 起点 |
| --- | --- |
| `today` | 今天 00:00 |
| `7d` | 今天往前 6 个日历日的 00:00（含今天共 7 天） |
| `30d` | 今天往前 29 个日历日的 00:00 |
| `all` | 零时间，全部 |

`Summarize(period)` 返回 `Summary`：窗口内的 `Totals`，一张按小时、天或周分桶的 `series`，以及按 agent、模型、上游密钥、订阅账号、调用方密钥、会话分组的 `Group`。分组键用改名之后的 provider id。会话组只包含写了 `session` 的行。

缓存命中率是 `cache_read / (in + cache_read + cache_write - cache_unknown)`。`cache_unknown` 的行表示厂商没给缓存计数，它的 prompt 不参加命中率。分母为 0 则没有命中率，不要显示 0%。

速度只统计被回答且 `ttft_ms > 0` 的行。平均首 token 是这些行的 `ttft_ms` 平均。生成速度用 `DecodeOf`：从约一成内容到九成内容的时间，避开开头和结尾的停顿。厂商一口气吐出整段、中间没有流动时间的回复，不产生速度。

`GET /api/usage?period=` 返回这个 `Summary`，并带上读者时区，用来标每天和每小时。

用量页上的「清除」不删 `usage.jsonl`。它在设置里记下「这个供应商的这个模型，这个时刻之前的行不再出现在用量页」。密钥花费限额和配额估算仍然数这些行。要真删行是另一条写路径，删除必须让块索引和 `log-v1.bin` 失效。

## 页面和限额怎么读

用量页的路由在 `usageRoutes`：

| 方法 | 路径 | 读什么 |
| --- | --- | --- |
| GET | `/api/usage?period=` | `Summarize` |
| GET | `/api/usage/requests` | 窗口内的行，新的在前，可按 agent、供应商、模型过滤 |
| GET | `/api/usage/requests.csv` | 同一批行的导出 |
| GET | `/api/usage/heatmap` | 按天的调用密度 |
| POST | `/api/usage/clear` | 只隐藏页面上的历史，见上 |
| GET | `/api/usage/quotas` | 向各厂商现拉的额度，不是账本加总 |

密钥限额走 `budget.Append`。没有 `callerKeyId` 时它只调用 `usage.Append`。有密钥时，它在同一把锁里先追加文件，再把这一行加进该密钥已经打开的窗口合计，避免「文件里有了、内存窗口还没有」或反过来被算两次。短时密钥的花费同时算进 `callerIssuerId`。`reported`、探针、取消、本地拒绝不占限额；`handed_on` 的 token 占。

`GET /api/usage/quotas` 是另一份数据：对各订阅和密钥余额发请求，得到窗口剩余。账本可以用来估计「整个窗口有多大」（已用加上厂商说的剩余），但剩余本身以厂商当场的回答为准。拉数失败不得改账本。

## 照着实现时的验收

用一个临时目录里的 `usage.jsonl`，不要碰真实配置。下面每条都应当有测试。

1. 追加一行后再读，字段与写入的 `Record` 一致。写盘失败时调用方拿到的仍是原来的 HTTP 结果。
2. 半行（进程在写换行前退出）被跳过，不使后续行解析失败。
3. 一次请求两次尝试：第一试产生了 token 然后失败，第二试成功。文件里两行，`route_id` 相同。汇总的调用次数是 1，token 是两行之和，失败次数是 0，重试次数是 1。
4. 第一试失败且 token 全是 0：文件里只有最终行。
5. `status` 499 且错误文案是 `the agent canceled the request`：算取消，不算失败，不算回答。
6. `source` 为 `probe`、`mcp`、`search` 的行不出现在 `Summarize` 的 `calls` 里。`reported` 出现在用量合计里，但不被密钥限额读到。
7. 费用用上面的公式。`cache_write_1h` 超出 `cache_write` 时按 `cache_write` 封顶。带 `cost` 的上报行忽略标价。
8. `today` 的起点是本地 0 点。0 点前一行、0 点后一行，只有后者进 `today`。
9. 供应商从 `a` 改名为 `b` 之后，改名之前的行在汇总里显示为 `b`，计价仍用 `a` 在该时刻的价。
10. 连续追加超过 512 行后重启进程，汇总与不使用 `log-v1.bin`、逐行重读的结果一致。
