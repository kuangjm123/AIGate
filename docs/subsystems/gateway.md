# Gateway

The gateway listener accepts a model request, checks a gateway token, and dials a provider with that provider's key. The caller uses a gateway token. The provider uses its own key. Neither secret is a console session.

[gateway.Server.ServeHTTP](../../internal/gateway/server.go) recognizes five dialects in [gateway.Classify](../../internal/gateway/dialect.go):

| Dialect | Request |
| --- | --- |
| Chat Completions | `POST /v1/chat/completions` or `POST /chat/completions` |
| Responses | `POST /v1/responses` or `POST /responses` |
| Anthropic Messages | `POST /v1/messages` or `POST /messages` |
| Gemini | `POST /v1beta/models/{model}:generateContent` or `:streamGenerateContent`, and the same call under `/models/{model}:` |
| Ollama | `POST /api/chat` |

An application can set its base URL to the gateway origin, such as `http://127.0.0.1:3425`. The application then appends the path for its own protocol, with or without the `/v1` prefix. Both forms are the same dialect.

`GET /api/hello` returns `{"name":"aigate"}` and does not require a token. `GET /v1/models`, `GET /models`, `GET /api/tags`, and `GET /v1beta/models` return the catalog. A route is listed by its `modelMatch`. A provider model that is not also a route name is listed as `providerId/model`. The entry carries the provider id, and the display name, context window, reasoning flag, and vision flag when those were set. Hidden models are omitted. Image and video models are omitted unless the query is `kind=image` or `kind=video`. A token with a model list sees only those ids. Subscription models from [accounts](accounts.md) are listed as `login/<account>/<model>` when that sign-in was read. A path that is not one of these returns `no_dialect`. That code is not `auth_failure`, `rate_limit`, `empty_balance`, or `upstream_unavailable`. Enabled plugin records in config are not turned into hooks.

## Request path

For a path that is not a console route:

1. Reject the request when [Store.Current](../../internal/state/store.go) is nil.
2. Admit the caller. The credential is the bearer token, or, when that header is absent, `x-api-key`, or, when that is absent, `x-goog-api-key`. An issued gateway token matches on every origin, and its id is stored. A remote peer, or a request with `X-Forwarded-For` or `Forwarded`, is `auth_failure` without one. A loopback peer with neither header may present any non-empty credential. `magpie`, `magpie-<name>`, `aigate`, and `aigate-<name>` are stored as the caller. Another local credential is not stored; the caller is the first word of the client identifier. An empty credential is `auth_failure`. The secret is not stored.
3. Serve a catalog `GET`, or enter the dialect path below. Any other path returns `no_dialect`.

A dialect request reads the model, the stream flag, and a session id from `x-session-id`, `x-claude-code-session-id`, `x-conversation-id`, or `conversation-id`. `x-aigate-kind: title`, `summary`, or `compact` is an ancillary request: it is still routed, and it does not run hooks, middleware, or the session stickiness below. `x-claude-code-agent-id` or `x-openai-subagent: collab_spawn` marks `kind` as `subagent`. Origin is `local` for a loopback peer with no `Forwarded` or `X-Forwarded-For` header, and `remote` otherwise. The hook context does not include the gateway token, the provider key, or those headers' values beyond the fields above.

Before a provider is chosen, each [gateway.RequestHook](../../internal/gateway/chain.go) may return a JSON object that adds context. It cannot reject the request. A hook that fails, times out, returns a non-object, or changes `model`, `stream`, or the tool declaration is ignored and does not hear the reply. The previous body stays. [gateway.Middleware](../../internal/gateway/chain.go) then may replace the body or reject. A rejection is returned in the caller's dialect and does not dial. A middleware failure leaves the body as it was.

A hidden model is refused with `hidden` and is not dialed. An image or video model on a conversation path is refused with `not_chat`. A token whose model list does not include the name is refused with `model_denied`. A request that carries an image or video is not sent to a provider model marked `vision: false`. When every candidate is marked that way, the caller gets `media` and no provider is dialed. The media is not removed from the body.

The model resolves to the first route whose `modelMatch` equals it, or to the one provider named by a `providerId/model` catalog id, or, when no route matches, to every provider that lists the model, in config order. The provider is asked for the model name it lists. The caller's catalog id stays on a translated reply. A session that already received a reply is tried on that provider first when the provider is still a candidate. Ancillary requests do not stick. A reply that has already started, including the first streamed byte, does not fall through to the next provider.

[gateway.Server.serveModel](../../internal/gateway/chain.go) then takes each candidate one at a time. It does not read the store again and does not hold the config lock.

1. An unknown provider, an unknown protocol name, a provider with no key, or a base URL [security.CheckBaseURL](../../internal/security/security.go) does not accept stops the loop. Those results do not fall through.
2. The body is translated into the provider's protocol. A provider with more than one key uses the next key when the one just tried returns 401 or 403. A network error stays on the key that was dialing. The same protocol is forwarded as the caller sent it, including tools, with the upstream model name written in. A different protocol is rewritten by `aiconv.DecodeTurn` and `aiconv.EncodeTurn`, including function tools, tool calls, tool results, and reasoning text. A tool declaration that does not decode as a function, a web search, or a Gemini tool skips that provider without a failure record. A provider protocol of `chat`, `openai`, `responses`, `anthropic`, `messages`, `gemini`, or `ollama` is recognized. An empty protocol means the provider speaks the caller's dialect. A cross-protocol stream keeps the caller's event shape for text, reasoning, and tool-call events.
3. Middleware `OnTry` may replace the body for this provider only. The next provider starts again from the body left after `OnRequest`.
4. The process dials with the provider key. The caller's gateway token is not forwarded. A redirect to another origin is not followed. A stream is written to the caller as events arrive. The same protocol is copied through. A different protocol's stream is `aiconv.ConvertStream` in the caller's event shape. A whole JSON reply in a different protocol is `aiconv.ConvertReply`. The caller's catalog id is the model written into that rewrite.
5. A completed reply stops the loop. Its body is the caller-visible reply. Middleware `OnReply` may replace a whole JSON reply. After the caller has the bytes, hooks that accepted the request are told in the background. A failed request, a stream that did not finish, or a reply the process did not keep whole is reported as no complete reply. That notice does not delay the caller.
6. On an eligible failure, try the next provider. Stop at the first completed reply or at the end of the list. The caller's error is the last failure that was not eligible to continue, in the caller's dialect.
7. Each dial appends a usage record. A skip that was only a protocol mismatch does not.

[gateway.Run](../../internal/gateway/run.go) is the attempt loop for a caller that supplies a [gateway.Dialer](../../internal/gateway/run.go). It resolves a route only, so a model that is only listed on a provider is `no_route` there. A nil dialer returns `no_dialect` and does not dial. `ServeHTTP` does not call `Run`. `Run` calls [gateway.Hook](../../internal/gateway/run.go) values, which may reject with one of the four classes. The HTTP client applies [security.AllowRedirect](../../internal/security/security.go): a different origin is not followed, and a relative path stays on the saved origin.

## Errors

These classes stay separate. [Route.Allows](../../internal/state/config.go) is the only fall-through check.

| Class | Meaning | Falls through |
| --- | --- | --- |
| `auth_failure` | The provider rejected its key, or a hook rejected with this class | Only when the route lists it |
| `rate_limit` | The provider asked the gateway to slow down | Only when the route lists it |
| `empty_balance` | The provider reported that it has nothing left to spend | Only when the route lists it |
| `upstream_unavailable` | The provider could not be reached, or the dialer returned an error | Only when the route lists it. That is the default when `eligible` is omitted |

Caller-side `auth_failure` from a missing gateway token is handled in `ServeHTTP` and does not enter the loop. A provider response of 401 or 403 is `auth_failure`, 429 is `rate_limit`, 402 is `empty_balance`, and 408 or any 5xx is `upstream_unavailable`. Any other provider status is returned in the caller's dialect and does not fall through.

## Usage

The record is the [AttemptRecord](../../internal/state/usage.go) described in [state](state.md). Token counts are the provider's own usage object when the reply included one. A whole JSON reply uses that object's `usage` field, or Gemini's `usageMetadata`. A stream takes the object from the provider's events: the last event that carries `usage` for chat and for responses (`response.usage` on a responses event), Gemini's last `usageMetadata`, and for Anthropic the `message.usage` from `message_start` with the later `usage` laid over it. A missing object is omitted, not stored as zero. Ollama does not send a usage object; its final counts stay omitted. A dialect attempt also stores the model, the session id, the gateway token id when the caller presented one, the caller name, and the caller's protocol. The token id is not the secret. Each attempt is its own line, so a fall-through leaves one line per provider with that provider's outcome.

When that call brought a session id and the reply finished, the gateway files one turn under the session. The turn's output is the client-visible text. A stream is joined into that one string before it is filed. A call with no session id is not filed. An ancillary request is not filed. A stream that does not finish, or a reply the process did not keep whole, is not filed as a finished output. The record is [state.TurnRecord](../../internal/state/conversation.go).

## Plugins

A plugin row is stored, enabled or disabled, and ordered. [gateway.Server](../../internal/gateway/server.go) does not load it. `ServeHTTP` calls the [gateway.RequestHook](../../internal/gateway/chain.go) and [gateway.Middleware](../../internal/gateway/chain.go) values the host passed in. The binary passes none. `Run` calls only the [gateway.Hook](../../internal/gateway/run.go) values the caller passed.

A hook does not choose the dial address and does not open a connection.

## Not in this process

How a plugin is loaded is open. See [gateway design](../design/gateway.md). Coding-agent config for the agents the console can connect is [agent wiring](agent-wiring.md). This process does not load a plugin host, and does not call a model for a title, a search, an image description, or a memory of its own. It does not offer those as tools. Subscription accounts are not a second credential on a provider. There is no quota, rate, or pause ledger on a token. A session id from the request is stored, and a finished reply is filed under that id. The process does not open an agent's transcript files to join that session. An application cannot append its own direct vendor calls to the usage file.

Verification is the command list in [AGENTS.md](../../AGENTS.md).
