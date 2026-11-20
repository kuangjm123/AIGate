# Agent wiring

The console Agent page detects coding agents on this computer. Connecting one writes that agent's own config so its model calls go to this gateway, and records the values it replaced. Disconnecting writes those recorded values back. Other fields in the same file are left as they are, including fields the user edits after connecting.

The notes of a connection are not in the config document. They are `agents/<id>.json` in the directory that contains the config file. A failed read of that note, or of the agent file, is `unknown`. Connect and disconnect then write nothing. A missing note is not a connection: disconnect does not guess the previous config.

A second connect keeps the first note. Reconnect rewrites the gateway values and does not replace the saved previous values. If the agent file no longer matches what connect wrote, or a saved URL is for a different gateway origin than the one listening now, the state is `drift`.

The value written as the key is `aigate-<id>`. It is the local caller name, not an upstream provider key. On a loopback peer it is accepted and stored as the caller. A remote peer, or a request with `X-Forwarded-For` or `Forwarded`, is `auth_failure` unless the credential is an issued gateway token. `magpie` and `magpie-<name>` are still caller names.

Connect refuses, and writes nothing, when the gateway is not listening (`not_listening`), when there is no conversation model to offer (`no_models`), when the agent is not installed (`absent`), or when the file is not one this process can edit without dropping other values (`unknown`). A hidden, image, or video model is not offered. A route name is offered as itself. A provider model is offered as `providerId/model`.

Cursor is detected and not edited. Its state is `manual`.

How a plugin is loaded, and which further agents get a migration, stay open. See [gateway design](../design/gateway.md).

## Agents

| Agent | Detected by | File this process writes | What is written |
| --- | --- | --- | --- |
| Claude Code | `claude` on `PATH`, or `~/.claude/settings.json`. A `.claude` directory that only has `.credentials.json` is not an install | `~/.claude/settings.json` | `env.ANTHROPIC_BASE_URL` is the gateway origin. `env.ANTHROPIC_AUTH_TOKEN` is `aigate-claude`, sent as `Authorization: Bearer`. `env.CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` is `1`. `model` is one offered catalog id. These are the fields in [Claude Code's gateway guide](https://code.claude.com/docs/en/llm-gateway-connect) and [settings reference](https://code.claude.com/docs/en/settings-reference) |
| Codex | `codex` on `PATH`, or `~/.codex/config.toml`. `auth.json` alone is not an install | `~/.codex/config.toml` | Top-level `model` and `model_provider = "aigate"`. The table `[model_providers.aigate]` has `name`, `base_url` (gateway origin plus `/v1`), `wire_api = "responses"`, and `experimental_bearer_token = "aigate-codex"`. Codex documents that bearer field, and `responses` as the only wire API. A file with a multiline string, a duplicate top-level `model`, or another `model_providers.aigate` form is `unknown` and is not rewritten |
| WorkBuddy | `workbuddy` on `PATH`, `WorkBuddy.exe` under an install root, or `~/.workbuddy/models.json` | `~/.workbuddy/models.json` | One entry per offered model whose id is not already used by another entry. `url` is the gateway origin plus `/v1/chat/completions`. `apiKey` is `aigate-workbuddy`. `vendor` is `Custom`, and `useCustomProtocol` is false, which is the custom-model shape this file uses. `supportsToolCall` is true. `supportsImages` and `supportsReasoning` follow the catalog when those were set, and `maxInputTokens` is the catalog context when one was set. An existing entry is left in place. A file that is a JSON array is that list. A file that is an object keeps its other fields and uses its `models` array; `availableModels` gains the new ids and is put back on disconnect. Any other JSON is `unknown`. When every offered id is already in the file, connect returns `id_taken` and writes nothing |
| OpenCode | `opencode` on `PATH`, or `opencode.json` / `opencode.jsonc` under `~/.config/opencode` (`XDG_CONFIG_HOME/opencode` when that variable is set) | `opencode.json`, or the existing `opencode.jsonc` when it is a JSON object | `providers.aigate` uses package `@opencode/ai/providers/openai-compatible`, `settings.baseURL` (origin plus `/v1`), `settings.apiKey` `aigate-opencode`, and one `models` entry per offered model. `model` is `aigate/<key>`. A model id that contains a slash is `modelID`. A project `opencode.json` can override this file |
| Gemini CLI | the `gemini` command on `PATH`. A `~/.gemini` directory alone is not an install | `~/.gemini/.env` and `model.name` in `~/.gemini/settings.json` | `GOOGLE_GEMINI_BASE_URL` is the gateway origin. `GEMINI_API_KEY` is `aigate-gemini`, which Gemini sends as `x-goog-api-key`. `GEMINI_MODEL` and `model.name` are a catalog id that contains no slash. Gemini loads the first `.env` it finds from the working directory upward, and does not replace a variable already set in the environment, so a project `.env` can still win |
| Aider | `aider` on `PATH`, or `~/.aider.conf.yml` | `~/.aider.conf.yml` | Top-level `openai-api-base` (origin plus `/v1`), `openai-api-key` `aigate-aider`, and `model` `openai/` plus the catalog id. Aider loads the home file first and a later project file overrides the same keys. A nested value for one of these keys is `unknown` |
| Cursor | `cursor` on `PATH`, or `Cursor.exe` / `Cursor.app` under the install roots passed in | None | The page tells the user to set Override OpenAI Base URL to the gateway origin plus `/v1` and the key `aigate-cursor`. That override covers OpenAI-format calls only |

A JSON file that is not one JSON object is `unknown`. Connecting a JSON file rewrites that file with the same values plus the fields above. A file this process created, and that disconnect leaves empty, is removed.

## API

`GET /agents` returns every agent above. `POST /agents/{id}/connect` and `POST /agents/{id}/disconnect` do the write. The response does not include a credential that was in the file before. The caller-facing codes are on the [console](console.md) page.

## Verification

`go test ./internal/agent/ ./internal/gateway/ ./internal/console/` covers a temp home. The tests do not read a live agent config. `TestClaudeConnectKeepsOtherFieldsAndRestores` fails if connect drops another field or disconnect fails to put the previous model back. `TestUnreadableStashDoesNotChangeTheAgent` fails if a bad note is treated as "not connected". `TestCodexUnsafeFileIsNotOverwritten` fails if a multiline Codex file is rewritten. `TestCursorIsNotEdited` fails if Cursor's connect writes a file. `TestLocalAigateKeyAndVendorHeadersAreTheCaller` fails if `aigate-<name>`, `x-goog-api-key`, or `x-api-key` is not admitted.
