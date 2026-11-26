# Gateway

The gateway listener accepts a model request, checks a gateway token, and dials one seat: one upstream key or one subscription account. The caller uses a gateway token. The provider uses its own key. Neither secret is a console session.

[gateway.Server.ServeHTTP](../../internal/gateway/server.go) recognizes five dialects in [gateway.Classify](../../internal/gateway/dialect.go):

| Dialect | Request |
| --- | --- |
| Chat Completions | `POST /v1/chat/completions` or `POST /chat/completions` |
| Responses | `POST /v1/responses` or `POST /responses` |
| Anthropic Messages | `POST /v1/messages` or `POST /messages` |
| Gemini | `POST /v1beta/models/{model}:generateContent` or `:streamGenerateContent`, and the same call under `/models/{model}:` |
| Ollama | `POST /api/chat` |

An application can set its base URL to the gateway origin, such as `http://127.0.0.1:3425`. The application then appends the path for its own protocol, with or without the `/v1` prefix. Both forms are the same dialect.

`GET /api/hello` returns `{"name":"aigate"}` and does not require a token. `GET /v1/models`, `GET /models`, `GET /api/tags`, and `GET /v1beta/models` return the catalog. A route is listed by its `modelMatch`, and a group is listed by its id with that id as both owner and bare name. A provider model is listed as `providerId/model`; that entry is omitted when the bare model is already a route or group id. The entry carries the owner id, and the display name, context window, reasoning flag, and vision flag when those were set. Hidden models are omitted. Image and video models are omitted unless the query is `kind=image` or `kind=video`. A token with a model list sees only those ids. Subscription models from [accounts](accounts.md) are listed as `login/<account>/<model>` when that sign-in was read. A path that is not one of these returns `no_dialect`. That code is not `auth_failure`, `rate_limit`, `empty_balance`, or `upstream_unavailable`. Enabled plugin records in config are not turned into hooks.

## Request path

For a path that is not a console route:

1. Reject the request when [Store.Current](../../internal/state/store.go) is nil.
2. Admit the caller. The credential is the bearer token, or, when that header is absent, `x-api-key`, or, when that is absent, `x-goog-api-key`. An issued gateway token matches on every origin, and its id is stored. A remote peer, or a request with `X-Forwarded-For` or `Forwarded`, is `auth_failure` without one. A loopback peer with neither header may present any non-empty credential. `magpie`, `magpie-<name>`, `aigate`, and `aigate-<name>` are stored as the caller. Another local credential is not stored; the caller is the first word of the client identifier. An empty credential is `auth_failure`. The secret is not stored.
3. Serve a catalog `GET`, or enter the dialect path below. Any other path returns `no_dialect`.

A dialect request reads the model, the stream flag, and a session id from `x-session-id`, `x-claude-code-session-id`, `x-conversation-id`, or `conversation-id`. `x-aigate-kind: title`, `summary`, or `compact` is an ancillary request: it is still routed, and it does not run hooks, middleware, or the session stickiness below. `x-claude-code-agent-id` or `x-openai-subagent: collab_spawn` marks `kind` as `subagent`. Origin is `local` for a loopback peer with no `Forwarded` or `X-Forwarded-For` header, and `remote` otherwise. The hook context does not include the gateway token, the provider key, or those headers' values beyond the fields above.

Before a provider is chosen, each [gateway.RequestHook](../../internal/gateway/chain.go) may return a JSON object that adds context. It cannot reject the request. A hook that fails, times out, returns a non-object, or changes `model`, `stream`, or the tool declaration is ignored and does not hear the reply. The previous body stays. [gateway.Middleware](../../internal/gateway/chain.go) then may replace the body or reject. A rejection is returned in the caller's dialect and does not dial. A middleware failure leaves the body as it was. A caller that cancels during either stage returns 499 and writes the request's canceled final usage line.

A hidden model is refused with `hidden` and is not dialed. An image or video model on a conversation path is refused with `not_chat`. A token whose model list does not include the name is refused with `model_denied`. A request that carries an image or video is not sent to a provider model marked `vision: false`. When every candidate is marked that way, the caller gets `media` and no provider is dialed. The media is not removed from the body.

The model resolves in this order:

1. An explicit `login/<account>/<model>` names that subscription account. It is one seat.
2. A group id, or the alias `group/<id>`, names a group. [gateway.planGroup](../../internal/gateway/group.go) orders every member's own seats first, then orders the member heads with the group strategy. A member is `providerId/model` or another group. A nested group keeps its own strategy; an empty inner strategy inherits `smart`, `rotate`, `usage`, `pace`, `free`, `fast`, or `drain` from the outer group, while an outer `order` or `weight` leaves the inner group on `smart`. The group's strategy memory is keyed `group/<id>`. A member's provider-level backup models do not join the group.
3. A route whose `modelMatch` equals the name.
4. A `providerId/model` catalog id.
5. A bare model listed by providers and readable subscription accounts. With more than one provider and automatic groups enabled, this becomes an implicit group in provider config order; readable subscription accounts that list the model join the same group as extra candidate heads for the outer strategy. With `noAutoGroups` true, it is `no_route` instead of silently choosing one provider.

The provider is asked for the model name it lists. The caller's catalog id stays on a translated reply.

The route's `routing` picks the strategy that orders its key and account seats; a group's `routing` orders its member heads. Empty is `smart`, and `order`, `rotate`, `usage`, `pace`, `weight`, `free`, `fast`, `cheap`, `drain`, and `expiring` are served by [routes.Builtin](../../internal/routes/routes.go). `cheap` and `expiring` are valid only on a group. A strategy is pure ordering over [routes.Seat](../../internal/routes/routes.go): it never dials and never waits, and [routes.Apply](../../internal/routes/routes.go) falls back to the listed order on a panic or an invalid permutation. An id this build does not serve also leaves the listed order. The gateway fills only the fields the strategy declared: send volume and reply speed are decayed in-memory averages fed by answered calls (about 1h and 20m half-lives). Reply speed is two readings per seat and model: first-token milliseconds `TTFT`, measured from the attempt start to the first byte written to the client on a streamed reply (a whole JSON reply does not update it), and output tokens per second `TPS` after that first byte. Prices come from the `Prices` catalog with the provider or key multiplier applied. A subscription account contributes only the quota windows that constrain the asked model; a failed or missing read stays unknown rather than zero, and an account whose reply will report its remainder gets one learning attempt. A key seat also compares a vendor balance only against seats of the same provider and unit.

Each seat key is stable for the process: `provider/<id>/key/<fingerprint>` for an upstream key and `login/<account>` for a subscription account. A 429 rests that seat for a minute, an unavailable one for 30 seconds, and a 402 marks it spent and records a drain refusal on the pool. A successful answer clears a rest but not `spent`. Resting seats try after the ready ones, and spent or drained seats try after those. A session that already received a reply is tried on that seat first when it is still a candidate and not resting or spent; when stickiness overrides a `weight` pick, [routes.Builtin.MoveRound](../../internal/routes/round.go) charges the round to the seat actually used. Ancillary requests do not stick. A reply that has already started, including the first streamed byte, does not fall through to the next seat.

[gateway.Server.serveModel](../../internal/gateway/chain.go) then takes each candidate one at a time. It does not read the store again and does not hold the config lock.

1. An unknown provider, an unknown protocol name, a provider with no key, or a base URL [security.CheckBaseURL](../../internal/security/security.go) does not accept stops the loop. Those results do not fall through.
2. The body is translated into the provider's protocol. One attempt uses exactly one key seat or account seat; a key is never swapped inside the dial. The same protocol is forwarded as the caller sent it, including tools, with the upstream model name written in. A different protocol is rewritten by `aiconv.DecodeTurn` and `aiconv.EncodeTurn`, including function tools, tool calls, tool results, and reasoning text. A tool declaration that does not decode as a function, a web search, or a Gemini tool skips that seat without a failure record. A provider protocol of `chat`, `openai`, `responses`, `anthropic`, `messages`, `gemini`, or `ollama` is recognized. An empty protocol means the provider speaks the caller's dialect. A cross-protocol stream keeps the caller's event shape for text, reasoning, and tool-call events.
3. Middleware `OnTry` may replace the body for this seat only. A model it writes is the model recorded on that attempt's usage line. The next seat starts again from the body left after `OnRequest`.
4. The process dials with the provider key. The caller's gateway token is not forwarded. A redirect to another origin is not followed. A stream is written to the caller as events arrive. The same protocol is copied through. A different protocol's stream is `aiconv.ConvertStream` in the caller's event shape. A whole JSON reply in a different protocol is `aiconv.ConvertReply`. The caller's catalog id is the model written into that rewrite.
5. A completed reply stops the loop. Its body is the caller-visible reply. Middleware `OnReply` may replace a whole JSON reply. After the caller has the bytes, hooks that accepted the request are told in the background. A failed request, a stream that did not finish, or a reply the process did not keep whole is reported as no complete reply. That notice does not delay the caller.
6. On a failure before the caller has received any reply content, try the next seat. Another key of the same provider, or another account of the same subscription family, is always tried. Crossing to another provider follows a common group's `eligible` list, or the route's `eligible` list when no group covers both seats. Stop at the first completed reply or at the end of the list. The caller's error is the last failure that was not eligible to continue, in the caller's dialect.
7. Each dial appends a usage record. A skip that was only a protocol mismatch does not.

[gateway.Run](../../internal/gateway/run.go) is the attempt loop for a caller that supplies a [gateway.Dialer](../../internal/gateway/run.go). It resolves a route only, so a model that is only listed on a provider is `no_route` there. A nil dialer returns `no_dialect` and does not dial. `ServeHTTP` does not call `Run`. `Run` calls [gateway.Hook](../../internal/gateway/run.go) values, which may reject with one of the four classes. A completed dial whose actual status is outside 2xx is a failed final line, not a success, and its status is kept. The HTTP client applies [security.AllowRedirect](../../internal/security/security.go): a different origin is not followed, and a relative path stays on the saved origin.

## Concurrency and RPM

A provider key seat may set `maxConcurrent`, `maxRPM`, `queueSlots`, and `queueWaitMS`. Zero means no cap for concurrency and RPM, and the process defaults for the queue length (8) and wait (10s). Subscription accounts have no such limits in this build.

The gate runs after the strategy order and before the dial. `maxConcurrent` counts requests in flight on that key; `maxRPM` counts request starts in the last minute. If the first ready seat is full and a later ready seat is free, the request moves to the later seat instead of waiting. A resting or spent seat is not a free alternative while ready seats remain. When every remaining ready seat is full, the request waits in their queues up to the configured wait, then returns `rate_limit` 429. A queue refusal does not rest the seat. The queue wait is recorded in usage `queue_ms`.

`POST /seats/reset` on the console accepts `{"seat":"provider/<id>/key/<fingerprint>"}` or a login seat, with an optional `scope`. It clears that seat's `spent` and drain refusal; it leaves a rest alone. Without a scope it clears the drain refusal on every pool. See [console](console.md).

## Errors

These classes stay separate. Between different vendors, [Route.Allows](../../internal/state/config.go) or [Group.Allows](../../internal/state/config.go) is the fall-through check. A sibling key of the same provider, or a sibling account of the same subscription family, does not consult that list.

| Class | Meaning | Falls through |
| --- | --- | --- |
| `auth_failure` | The provider rejected its key, or a hook rejected with this class | Another key of the same provider, or a group or route that lists it |
| `rate_limit` | The provider asked the gateway to slow down | Another key of the same provider, or a group or route that lists it |
| `empty_balance` | The provider reported that it has nothing left to spend | Another key of the same provider, or a group or route that lists it. The seat is also spent and drained |
| `upstream_unavailable` | The provider could not be reached, or the dialer returned an error | Another key of the same provider, or a group or route that lists it. That is the default when `eligible` is omitted |

Caller-side `auth_failure` from a missing gateway token is handled in `ServeHTTP` and does not enter the loop. A provider response of 401 or 403 is `auth_failure`, 429 is `rate_limit`, 402 is `empty_balance`, and 408 or any 5xx is `upstream_unavailable`. Any other provider status is returned in the caller's dialect and does not fall through.

## Usage

The boundary of what an attempt line is for is [usage](../usage.md).

The record is the [AttemptRecord](../../internal/state/usage.go) described in [state](state.md). One user request writes one final line. Every attempt of that request shares a fresh `routeId`; finished attempts carry `tries` when there was more than one. An attempt that had already produced tokens and failed before another provider was tried writes a separate `handedOn` line, with its own start, finish, duration, tokens, and byte counts. An attempt that produced no tokens does not write a line; the final line is the only outcome. A failed attempt that is already the last candidate is the final line, not a `handedOn` line, so its tokens are counted once. A 499 whose error is `the agent canceled the request` is a cancellation.

Token counts are the provider's own usage object when the reply included one. A whole JSON reply uses that object's `usage` field, or Gemini's `usageMetadata`. A stream takes the object from the provider's events: the last event that carries `usage` for chat and for responses (`response.usage` on a responses event), Gemini's last `usageMetadata`, and for Anthropic the `message.usage` from `message_start` with the later `usage` laid over it. A missing object is omitted, not stored as zero. Ollama does not send a usage object; its final counts stay omitted. Anthropic's one-hour cache creation count fills `cacheWrite1h`. The line also stores the upstream HTTP status, the whole-request or per-handoff duration in milliseconds, first-byte and first-text milliseconds on streams, queue and retry milliseconds, and request/reply body bytes. A dialect attempt stores the model, session id, caller key id, issuing key id, upstream key fingerprint, caller name, agent, and caller's protocol. None of those ids is a secret.

A gateway token may store up to five `limits`, plus the older `spendLimit`/`spendPeriod` cap. Each cap may count dollars, tokens, or both, in a local-day/week/month/all or rolling hours/days window, optionally scoped to a provider and model. A zero or omitted amount does not cap that field; a zero legacy `spendLimit` is no cap. Before a model call, [gateway.Server.spendBlock](../../internal/gateway/spend.go) adds the lines charged to that token: its own calls, plus, for a short-lived key, the calls of every key it issued. Reaching any cap returns `spend_limit` (429) and does not dial. A cap whose price list cannot be read, or a billable row with no price, returns `price_unknown` (503), which is not treated as zero spend. `handedOn` tokens count toward a cap; reported, probe, MCP, search, canceled, and locally rejected rows do not. A token with no cap is not checked.

`POST /v1/magpie/usage/requests` lets a client report a call it made directly to a vendor. The line is forced to `source: reported`, increments the usage page totals, and does not consume a gateway token cap. A request id already present on any source's line, including a gateway-written line, is acknowledged but not written twice. The request shape and fields are in [usage](../usage.md).

`GET /usage/traffic` reads process-memory counters only: in-flight model requests and bytes read from callers or written to them since process start. No ledger line is read. `GET /usage/requests/content` reads the conversation archive for a session; the usage line itself has no prompt or reply text.

When that call brought a session id and the reply finished, the gateway files one turn under the session. The turn's output is the client-visible text. A stream is joined into that one string before it is filed. A call with no session id is not filed. An ancillary request is not filed. A stream that does not finish, or a reply the process did not keep whole, is not filed as a finished output. The record is [state.TurnRecord](../../internal/state/conversation.go).

## Plugins

A plugin row is stored, enabled or disabled, and ordered. [gateway.Server](../../internal/gateway/server.go) does not load it. `ServeHTTP` calls the [gateway.RequestHook](../../internal/gateway/chain.go) and [gateway.Middleware](../../internal/gateway/chain.go) values the host passed in. The binary passes none. `Run` calls only the [gateway.Hook](../../internal/gateway/run.go) values the caller passed.

A hook does not choose the dial address and does not open a connection.

## Not in this process

How a plugin is loaded is open. See [gateway design](../design/gateway.md). Coding-agent config for the agents the console can connect is [agent wiring](agent-wiring.md). This process does not load a plugin host, and does not call a model for a title, a search, an image description, or a memory of its own. It does not offer those as tools. A subscription account is its own seat, not a second credential on a provider. A session id from the request is stored, and a finished reply is filed under that id. The process does not open an agent's transcript files to join that session. Direct vendor calls enter the ledger only through the explicit reported endpoint, which never joins routing or a gateway token cap.

Verification is the command list in [AGENTS.md](../../AGENTS.md).
