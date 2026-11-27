# Reasoning effort

The gateway maps the thinking level a coding agent asks for onto the fields one upstream API accepts. The mapping is per attempt: a request that falls through to another provider is fitted again to that provider's model and protocol. This page covers request rewriting and the model metadata that feeds it. It does not cover context windows, compaction, service tiers, routing, or usage pricing.

The pure level functions are in [levels.go](../../internal/reasoning/levels.go), request readers in [signals.go](../../internal/reasoning/signals.go), and vendor writing in [apply.go](../../internal/reasoning/apply.go).

## Levels

The ordered level names are `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`, and `ultra`. `light` is another spelling of `low`, and `extra_high` of `xhigh`; case and surrounding whitespace are ignored. `off` is not a level. Only the Chat and Responses readers turn `off` into `none`.

Three rules are shared by the vendor writers:

- `fit` turns `ultra` into `max`, removes `ultra` from the model's list, and chooses the nearest listed level. A tie goes to the stronger level, and `none` is skipped unless it was requested.
- A model known to have no selectable levels cannot receive a level above `high`. A model whose levels are merely unknown keeps the requested level.
- A token budget is the fallback only when the request has no level name. `low`, `medium`, `high`, `xhigh`, and `max` map to 4096, 10000, 24000, 32000, and 32000 tokens. A 32000 budget reads back as `xhigh`; a vendor that distinguishes it from `max` makes that change itself.

## Reading a request

[reasoning.Read](../../internal/reasoning/signals.go) reads the accepted inbound body once, after request middleware and before routing. It keeps the named level, a derived budget level, and the off state as separate facts:

| Dialect | Fields read |
| --- | --- |
| Chat Completions | `reasoning_effort`, `reasoning.effort`, Chat `thinking.budget_tokens`, and a relay's `extra_body.google.thinking_config` or `extraBody.google.thinkingConfig` |
| Responses | `reasoning.effort`, `reasoning.budget_tokens`, and `reasoning_effort` |
| Anthropic Messages | top-level and last-message `output_config.effort`, plus `thinking.type` and `thinking.budget_tokens` |
| Gemini | `generationConfig.thinkingConfig.thinkingLevel` and `thinkingBudget`, including snake_case keys |
| Ollama | `think` as a boolean or level |

The reader keeps `thinkOff` separate from the level string. Chat and Responses `off`, an Anthropic `thinking.type` of `disabled`, Gemini budget `0`, and Ollama `think: false` set that flag, so the writer does not collapse the request to a low thinking budget. A Chat relay's Google budget of `-1` means off, `0` means `low`, and a positive budget becomes a level.

## Model metadata

[state.ModelInfo](../../internal/state/config.go) carries `reasoningEfforts`, the model's own level names. The [custom JSON codec](../../internal/state/modelinfo.go) also accepts `reasoning_efforts`, `reasoningEffort`, `reasoning_effort`, and `levels`. An absent field means the levels are unknown. An explicit `[]` means the catalog said the model has no level selector; that is different from not having read a list yet.

Provider model entries, readable subscription models, Codex sign-in models, and WorkBuddy's `reasoningEffort` list feed this metadata. [codexcat.Catalog](../../internal/codexcat/codexcat.go) writes it as `supported_reasoning_levels`, and [agent.Connect](../../internal/agent/codex.go) carries it into the local `aigate-models.json`. The gateway's `GET /v1/codex/models` uses the same renderer. The ordinary `/v1/models` shapes still carry only the `reasoning` flag.

For the Codex selector, a reasoning model whose levels are not known starts with `low`, `medium`, `high`, and `xhigh`; a model with a known list uses that list. A non-OpenAI reasoning model with a non-empty list also offers `ultra`. OpenAI-family ids offer `ultra` only when their own catalog listed it. `ultra` is selector-only: `ultra` is fitted to `max` before an upstream request. A known empty list adds no selector level, and the configured `model_reasoning_effort` remains the file's value.

## Group overrides

A [state.Group](../../internal/state/config.go) may set `levels` and `reasoningMap`. A member may carry a level suffix: `providerId/model:high` fixes one level, and `providerId/model:low+high` limits the levels available to that member. `off` in a suffix is stored as `none`. [gateway.planGroupWithExtra](../../internal/gateway/group.go) copies the top group's settings to its seats.

Per seat, the request level is resolved in this order:

1. A fixed member level replaces the client's level and resets the off state to whether that fixed level is `none`.
2. A member mapping rewrites the resulting level. A key without `/` applies to every member; a key prefixed with the member reference applies to that member. `ultra` is looked up first, then `max`.
3. A member's `low+high` list, otherwise the group's `levels` list, otherwise the target model's own list is the list passed to `fit`.

The group's list replaces a member model's full list. A member suffix narrows that member before the group list is considered. The mapping happens before fitting, so a mapped value is still constrained by the final list.

## Upstream rewriting

After the body is translated into the destination protocol, [gateway.serveModel](../../internal/gateway/chain.go) calls [reasoning.Rewrite](../../internal/reasoning/apply.go) for that attempt. Rewriting the translated body is required: a Chat request sent to Anthropic plus a Responses request sent to Anthropic both need Anthropic's own field, and the original client level must survive the protocol conversion. Model middleware `OnTry` sees the rewritten body.

The writer rules are:

- **OpenAI Chat and Responses.** Write `reasoning_effort` or `reasoning.effort`. `ultra` is never sent. When the destination is OpenAI's API or Azure, remove `enable_thinking` and `thinking`.
- **Adaptive Claude.** Claude 4.6 and later use `thinking.type = adaptive` and `output_config.effort`, with no budget. A 32000-token budget-only request becomes `max`. Opus 4.6 and Sonnet 4.6 turn a named `xhigh` into `max`; later versions preserve `xhigh`.
- **Older Claude.** A thinking request writes an enabled thinking budget, capped below `max_tokens` and with `max_tokens` raised when needed and no value was supplied. A `max_tokens` of 2048 or less does not receive a thinking block.
- **Claude off modes.** Sonnet 5.5 uses `between_tools` and caps `xhigh` or `max` at `high`; Opus 5.5, Fable 5, and Mythos 5.x remove thinking and write `low` when no level was supplied; Opus 5, Haiku 5.5, and older families use `disabled`.
- **DeepSeek's Anthropic endpoint.** Fit to the model's levels, or to `low`, `high`, and `max` when they are unknown. `minimal` becomes `low`, `medium` and `xhigh` become `high`, and `ultra` becomes `max`. The level is also written as `output_config.effort`.
- **GLM 5.2 and 5.3.** Use adaptive thinking and `output_config.effort`; `max` remains `max`.
- **MiniMax M3.1 Flash and `gpt-<number>` Messages relays.** Write `output_config.effort` so a relay does not fall back to its own default.
- **Gemini 1 and 2.0.** Remove a thinking configuration.
- **Gemini 2.5.** Write `thinkingBudget`: Pro accepts 128 through 32768, Flash Lite 512 through 24576, and Flash 1 through 24576. Off writes the family's closed value and removes `includeThoughts`.
- **Gemini 3 and later on Google's API.** Write `thinkingLevel`. `minimal` stays; `none` and weaker levels become `low`; a level above `high` is kept only when the model lists something stronger. Vertex off uses `minimal` when the model lists it, otherwise `low`.
- **Gemini 3 and later through a relay.** Anthropic's enabled budget becomes adaptive thinking plus `output_config.effort`; Chat's Google budget becomes `thinking_level`. A relay with no model levels writes `high` for `medium` on a non-Flash Gemini 3 Pro. Budgets are not sent to Gemini 4.
- **Ollama.** Write the level, or `think: false` when the model has no selectable level.

If a Gemini-level request is rejected with 400 or 422 and the error names an invalid, unsupported, or unknown thinking level, the gateway rebuilds that attempt once with the lowest non-`none` level. This retry covers the native Gemini field and a relay's Chat or Anthropic field alike. A repeated rejection follows the normal error and fall-through rules.

## DashScope

DashScope rejects a non-streaming Chat request whose thinking is on. When the destination is DashScope, the caller asked for one JSON object, and thinking is on in the accepted request or rewritten body, [gateway.serveModel](../../internal/gateway/chain.go) asks the upstream for SSE. The gateway joins text, reasoning text, tool-call deltas, finish reason, and usage into one Chat reply. If the caller used another dialect, the joined Chat reply is converted to that dialect before it is returned. A non-stream reply does not set first-token latency.

## Boundaries

The gateway does not probe a model to discover levels. Unknown metadata stays unknown, and a known empty list stays empty. A route entry without explicit level metadata does not advertise a selector list; a group can supply one. The writer does not change a model id, tools, context limits, compaction, or service tier. It does not invent an off switch for a vendor that has none.

## Verification

Run the commands in [AGENTS.md](../../AGENTS.md). [apply_test.go](../../internal/reasoning/apply_test.go) covers the level functions, request readers, vendor tables, aliases, Vertex, relay budgets, and a mapping that turns off back on. [reasoning_test.go](../../internal/gateway/reasoning_test.go) covers per-attempt fitting, cross-protocol Claude, Responses `ultra`, Gemini budgets and level retry on the native and Chat-relay paths, fixed Gemini levels on an Anthropic relay, group overrides, DashScope streaming, cross-dialect aggregation, and tool calls. [reasoning_test.go](../../internal/state/reasoning_test.go) covers known-empty model metadata and group member suffixes. [codexcat_test.go](../../internal/codexcat/codexcat_test.go) and [agent_test.go](../../internal/agent/agent_test.go) cover the Codex selector and generated catalog.
