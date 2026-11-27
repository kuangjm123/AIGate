# 路由策略模块能力边界

> 文档定位：描述当前工作树已经运行的行为，不是新的契约。设计目标和未完成项见
> [Routing parity](design/routing-parity.md)，策略语义见
> [路由策略](design/routing-strategies.md)。子系统契约以
> [Gateway](subsystems/gateway.md)、[State](subsystems/state.md)、
> [Console](subsystems/console.md) 为准。
>
> 核对时间：2026-11-26，以当前工作树源码为准。

## 一句话结论

路由策略仍是**纯排序算法**，但网关已经按 `routing-parity` 第 1–5 步接好了数据
和尝试循环：

- 每把供应商密钥、每个可读订阅账号各是一个席位；
- 同一供应商内换席位不限于 401/403，429、402、5xx 和网络错误也会在首字节之前
  继续尝试；
- 订阅额度、余额、目录价格、实测速度、Spent/Drained、会话粘性和
  并发/RPM/队列都按席位计算；
- 路由组、嵌套组、自动组、`cheap`/`expiring` 组策略和
  `noAutoGroups` 开关已经可用；
- 控制台可以管理组、设置供应商并发限制，并显式解除一个席位的额度用尽。

仍未实现的是意图规则、慢启动、逐请求决策记录、可关闭的策略注册插件，以及
订阅账号自己的并发限制。它们不影响日常多密钥、多账号、多成员切换。

## 能力分层

| 层 | 位置 | 负责 | 不负责 |
| --- | --- | --- | --- |
| 策略库 | [`internal/routes`](../internal/routes/routes.go) | 接收 `[]Seat`，返回下标排列；11 种内置策略；轮转、权重、drain、速度探测的进程内状态；对 panic 和非法排列兜底 | 拨号、等待、读配置、读配额、持久化 |
| 网关策略接线 | [`internal/gateway/routing.go`](../internal/gateway/routing.go)、[`seat.go`](../internal/gateway/seat.go) | 把密钥和账号收成稳定席位；填策略声明要的字段；休息、spent、drain、粘性、权重轮次 | 改变策略算法 |
| 路由组 | [`internal/gateway/group.go`](../internal/gateway/group.go) | 先排成员内部席位，再按外层策略排成员头；传播外层可继承策略；保留嵌套边界 | 把成员自己的备用模型带进组 |
| 配额与限制 | [`quota.go`](../internal/gateway/quota.go)、[`limits.go`](../internal/gateway/limits.go) | 读订阅窗口和余额；按席位维护并发、RPM、队列；失败读取保持未知 | 把读不到写成 0 |
| HTTP 主链 | [`internal/gateway/chain.go`](../internal/gateway/chain.go) | 解析组/路由/目录候选；会话粘性；首字节前逐个席位尝试 | 已下发正文后回滚或改派 |
| 配置与控制台 | [`internal/state/config.go`](../internal/state/config.go)、[`internal/console/server.go`](../internal/console/server.go) | `Route`/`Group` 模型、校验、`/routes`、`/groups`、`/auto-groups`、`/seats` | 决策追踪、策略插件、内容分类 |

## 术语

- **路由（Route）**：`modelMatch` 到一组 `providerIds` 的规则，可带 `routing`、
  `weights` 和 `eligible`。
- **组（Group）**：自己有一个可被请求的 id；成员是 `供应商/模型` 或另一个组。
- **席位（Seat）**：一把供应商密钥或一个订阅账号。席位键是
  `provider/<id>/key/<指纹>` 或 `login/<账号>`。
- **池（Pool）**：策略记忆的键。路由用 `route.ID`；显式组用
  `group/<id>`；自动组和裸模型用 `model/<model>`。
- **降级类别（eligible）**：跨供应商继续尝试时允许的错误类别。同一供应商的
  多把密钥不受它限制。

## 一次请求如何变成席位

模型解析顺序：

1. 显式 `login/<account>/<model>` 只选该订阅账号，形成一个席位；
2. 组 id 或 `group/<id>` 进入 `planGroup`；
3. 第一条 `modelMatch` 完全相等的路由；
4. `provider/model` 目录 id 只得到该供应商；
5. 裸模型：列出它的供应商，加可读且已知模型的订阅账号。多于一家供应商且自动
   组开启时，先合成一个组；`noAutoGroups` 开启时返回 `no_route`，不偷偷选一家。

`planGroup` 对每个成员先排自己的席位：

- 供应商成员按外层继承后的策略给该供应商的多把密钥排序；
- 内层组按自己的策略给成员排序，再回到外层；
- 外层只看到每个成员的头，再用组的策略和权重排序这些头；
- 自动组还把同一裸模型的可读订阅账号作为额外候选头，和供应商成员头一起参与
  外层排序与失败转移；
- 每个席位带 `Scopes`，跨成员失败时只按共同组或路由的 `eligible` 判断。

组策略缺省时的继承规则：

| 外层策略 | 内层缺省 |
| --- | --- |
| `smart`、`rotate`、`usage`、`pace`、`free`、`fast`、`drain` | 继承外层 |
| `order`、`weight` | 内层按 `smart` |
| 无外层 | `smart` |

外层 `weight` 的键是组员而不是密钥，所以成员内部的密钥排序使用 Smart。

## 策略矩阵

“网关填充”列是当前生产路径真正提供的数据；未列出的字段保持未知。

| id | 语义 | 网关填充 | 备注 |
| --- | --- | --- | --- |
| `""` Smart | 有额度的席位先按最大窗口重置时间，再按积分和同单位余额 | `Used`、`Soon`、`Credits`、`Left`、`WeekLeft`、`Tokens` | 读不到额度是未知，不是 0 |
| `order` | 原样按列出顺序 | 不需要字段 | 休息和用尽席位仍排到后面 |
| `rotate` | 每个池一个进程内计数器，每次请求下移一位 | 不需要字段 | 粘性可覆盖；不修正轮转计数 |
| `usage` | 最少使用：学习账号、已报比例且有余量、未知、档 1、档 2 | 订阅窗口、余额、`Tokens` | `Tokens` 是约 1 小时半衰期的进程内发送量 |
| `pace` | 每周剩余比例除以距离重置的小时数 | `WeekLeft`、`WeekReset`、`Tokens`、余额 | 无周窗口的付费密钥按 0 速率 |
| `weight` | 对成员头做平滑加权轮转 | `weights`；组里是成员权重 | 粘性改派时用 `MoveRound` 修正 |
| `free` | 模型不花钱或账号方案像免费/试用时优先 | 目录价、供应商/密钥倍率、`FreePlan` | 现在会为它读订阅额度中的方案信息 |
| `fast` | 用 TTFT/TPS 估计短回复耗时，并定期探测未测席位 | `TTFT`、`TPS`、`MeasuredAt` | 非流式回复不产生 TTFT 样本 |
| `cheap` | 按当前输入+输出每百万 token 价格排序 | 价格簿、供应商/密钥倍率 | 只允许组 |
| `drain` | 停在一个席位直到用尽，再停在下一个 | `Used`、`Spent`、`Drained` | 429 和普通失败只是休息，不换位 |
| `expiring` | 免费额度按到期时间，付费按 Smart，用尽最后 | `Free`、`FreePlan`、`Expires`、Smart 字段 | 只允许组 |

仍未由网关填写的字段是 `Lapsing`、`Lapse`、`Unreached`、`First` 和 `Lead`。
当前订阅额度读取没有提供积分作废时间或“不可能用完”的判断，因此这些保持未知；
用户也没有“先花积分”的开关。

## 失败、休息与解除

上游结果到类别：

| 上游结果 | 类别 | 记忆 |
| --- | --- | --- |
| 401/403 | `auth_failure` | 只影响本次失败转移，不休息 |
| 429 | `rate_limit` | 该席位休息约 1 分钟 |
| 402 | `empty_balance` | 该席位 `Spent`，并在当前池记 `Drained` |
| 408/5xx/网络错误 | `upstream_unavailable` | 该席位休息约 30 秒 |
| 其它非 2xx | 无类别 | 直接按上游状态返回 |

失败后的顺序：

- 下一席位属于同一供应商的密钥，或同一 WorkBuddy 家族账号时，总是继续尝试；
- 跨供应商时，先看两个席位共同的最内层组；共同组允许该类别就继续；
- 没有共同组时看路由的 `eligible`；
- 客户端的回复已经开始写第一个内容字节后，不再改派。

`eligible` 三态：

- 缺省：只允许 `upstream_unavailable`；
- 显式列表：只允许列出的类别；
- 显式空数组：任何类别都不跨供应商降级。

休息是排到末尾，不是跳过。前面的席位都失败后，休息中的席位仍会被尝试。成功只
清掉该席位的休息，不清掉 `Spent`。`POST /seats/reset` 清除该席位的 `Spent` 和
drain 拒绝；不带 `scope` 时清除所有池上的 drain 拒绝。休息时间不会被这条接口
清掉。

## 会话粘性

客户端带 `x-session-id`、`x-claude-code-session-id`、`x-conversation-id` 或
`conversation-id` 时，网关记住成功应答的席位键。下一次同一会话的请求如果该席位
仍在候选里且没有休息/用尽，就把它提到第一。附属请求（标题、摘要、压缩）不做
粘性。粘性可以覆盖任何策略的顺序；只有 `weight` 在改派时修正本轮权重。

粘性、轮转、权重、drain 位置、速度样本和 rest/spent 都在进程内，重启归零。

## 并发、RPM 和队列

供应商的每个密钥席位可以使用：

- `maxConcurrent`：同时进行的请求上限；
- `maxRPM`：最近一分钟的请求起点上限；
- `queueSlots`：满员时最多几个请求等待，0 用默认 8；
- `queueWaitMS`：最多等待多久，0 用默认 10 秒。

这层在策略排序之后、拨号之前。当前第一个席位满员时，会向后找空闲且没有休息/
用尽的席位；不会为了绕开满员席位而跳到休息或 spent 席位。所有剩余可用席位都
满员时才进入有界等待，超时返回 `rate_limit` 429，队列等待写入 usage 的
`queue_ms`。队列拒绝不会把席位标成休息。

订阅账号目前没有并发/RPM 配置；它们是席位，但不经过这层限制。

## 路由组

组的配置字段和校验见 [State](subsystems/state.md)。运行时的要点：

- 组 id 就是调用方请求的模型名；`group/<id>` 是别名；
- 请求组时，目录里的组条目 owner 和 bare 都是组 id；
- 嵌套组可以有自己的策略和 `eligible`；
- 同一组员内部的密钥按继承后的策略排序，不把供应商自己的备用模型带进来；
- `cheap` 和 `expiring` 只允许配在组上，路由配这两个会被拒绝；
- 自动组只在“没有路由、没有显式组、裸模型被多家供应商列出”时生成，成员顺序
  跟供应商配置顺序；它不包含订阅账号；
- `noAutoGroups` 开启时，上述裸模型返回 `no_route`。

显式 `login/<account>/<model>` 仍是单席位请求，不会自动合并到同模型的其他账号；
裸模型没有路由且没有目录 id 时，才会把已知该模型的订阅账号放进同一个候选池。

## 控制面

| 能力 | 入口 |
| --- | --- |
| 路由 CRUD | `GET/POST /routes`、`PUT/DELETE /routes/{id}` |
| 组 CRUD | `GET/POST /groups`、`PUT/DELETE /groups/{id}`；组 id 可含 `/` |
| 自动组开关 | `GET/PUT /auto-groups`，`{"on":false}` 关闭 |
| 席位列表 | `GET /seats`；只返回指纹，不返回密钥或登录令牌 |
| 解除用尽 | `POST /seats/reset`，`{"seat":"...","scope":"..."}` |

控制台页面在“模型路由”里新建/编辑/删除组并切换自动组；在“模型提供方”里编辑
并发/RPM/队列，并在“席位”面板解除用尽。页面和 API 都使用控制台会话 Bearer，
不是网关令牌。

## 明确没有的能力

- **意图规则**：没有按请求内容选择强/便宜模型的分类器。
- **慢启动**：没有“首 token 超时就换成员且不算失败”的组选项。
- **逐请求决策记录**：没有候选顺序、休息状态、最终应答席位的独立 trace；只能
  从 usage 行间接推断。
- **可关闭的策略插件**：`Builtin.Find` 仍是硬编码 switch，配置里的 plugin 行
  不注册策略。
- **按请求把同供应商更贴合的密钥提前**：`routes.Exact` 存在，但网关没有调用。
- **订阅账号并发/RPM**：没有账号级配置。
- **通配/正则/内容匹配**：`modelMatch` 仍是完全相等，取第一条。
- **同一请求并行打多家**：只串行尝试，没有 hedged request。
- **持久化策略状态**：重启丢轮转、权重、drain、速度、rest、spent、粘性。
- **命令行建组/路由**：只有服务进程和控制台 API。

## 验证

与路由直接相关的测试：

- `internal/routes/routes_test.go`：策略算法、兜底、轮转、权重、drain、
  Speed、cheap、expiring。
- `internal/gateway/routing_test.go`：密钥级席位、429/402、休息过期、显式解除、
  并发队列和粘性。
- `internal/gateway/quota_test.go`：窗口过滤、订阅配额、余额单位、免费方案。
- `internal/gateway/group_test.go`：组内继续、内层顺序、第二把密钥、自动组、
  关闭自动组。
- `internal/state/state_test.go`：组往返、成员解析、校验和类别限制。
- `internal/console/server_test.go`：组 CRUD、自动组开关、供应商限制、席位列表和
  解除。

完整命令仍是 [AGENTS.md](../AGENTS.md) 中的格式、测试、竞态和构建列表。

## 源码索引

| 文件 | 内容 |
| --- | --- |
| [`internal/routes/routes.go`](../internal/routes/routes.go) | `Seat`、`Fields`、`Strategy`、`Apply`、`Builtin`、策略 id |
| [`internal/routes/strategies.go`](../internal/routes/strategies.go) | 除速度优先外的策略、drain、UnDrain |
| [`internal/routes/fastest.go`](../internal/routes/fastest.go) | 速度估算、分带和探测 |
| [`internal/routes/round.go`](../internal/routes/round.go) | 平滑加权轮转和 `MoveRound` |
| [`internal/gateway/routing.go`](../internal/gateway/routing.go) | `orderSeats`、席位填充、rest/spent、volume/speed、`ResetSeat` |
| [`internal/gateway/seat.go`](../internal/gateway/seat.go) | 供应商密钥、订阅账号和组员席位 |
| [`internal/gateway/group.go`](../internal/gateway/group.go) | `planGroup`、策略继承、作用域和自动组成员 |
| [`internal/gateway/quota.go`](../internal/gateway/quota.go) | 订阅窗口、余额、免费方案 |
| [`internal/gateway/limits.go`](../internal/gateway/limits.go) | 并发、RPM、队列和改派 |
| [`internal/gateway/chain.go`](../internal/gateway/chain.go) | 模型解析、粘性、尝试循环、失败转移 |
| [`internal/state/config.go`](../internal/state/config.go) | `Provider` 限制字段、`Route`、`Group`、成员解析 |
| [`internal/state/validate.go`](../internal/state/validate.go) | 路由、组、权重、类别和重复 id 校验 |
| [`internal/console/server.go`](../internal/console/server.go) | `/groups`、`/auto-groups`、`/seats` API |
| [`web/src/sections/Routes.jsx`](../web/src/sections/Routes.jsx) | 路由和组配置页面 |
| [`web/src/sections/Providers.jsx`](../web/src/sections/Providers.jsx) | 并发限制和席位解除页面 |
