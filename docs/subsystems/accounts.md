# Accounts

A subscription sign-in stays in the coding agent that saved it. The console lists it beside API-key providers. Connecting an agent only decides whether that agent's own requests are sent to the gateway.

[login.Discover](../../internal/login/login.go) reads Claude Code, Codex, GitHub Copilot, Gemini CLI, Antigravity, and WorkBuddy. `noAccountDiscovery: true` in the config reads none of those files. A missing file is signed out. A file that does not parse is unknown: the row stays and its model list is not treated as empty. Logging out of the agent removes the file, and the provider disappears. The token is not written into `config.json`.

An enabled plugin whose id is `claude`, `codex`, `copilot`, `gemini`, `antigravity`, `workbuddy`, or `workbuddy-ai` occupies that sign-in's slot. The process does not dial it. How a plugin is loaded is still open.

## Model lists

The list is not the one written when an agent is connected.

Claude Code uses the public model ids in [login.claudePublic](../../internal/login/claude.go), plus `availableModels`, `modelPicker.options`, and `modelOverrides` in `~/.claude/settings.json`. The sign-in is not sent to Anthropic to ask for models.

Codex with `auth_mode` `chatgpt` sends the ChatGPT access token to `https://chatgpt.com/backend-api/codex/models`. A model the backend does not list is not offered. A failed read leaves the model list unknown. Codex with `auth_mode` `apikey` uses `OPENAI_API_KEY` from the same `auth.json` and reads `https://api.openai.com/v1/models`. The key is not copied into config.

Copilot exchanges the token in `~/.config/github-copilot/hosts.json` for a session token and reads that session's model list. Gemini CLI and Antigravity use the public Gemini model ids when `oauth_creds.json` holds an access token.

WorkBuddy has two desktop sign-ins. China is `workbuddy-desktop.info` and the international build is `workbuddy-desktop-ai.info`, beside each other. On Windows that directory is `%LOCALAPPDATA%\CodeBuddyExtension\Data\Public\auth`. On macOS it is `~/Library/Application Support/CodeBuddyExtension/Data/Public/auth`. On Linux it is `~/.local/share/CodeBuddyExtension/Data/Public/auth`. The file is only read. A string `auth.accessToken` is a sign-in. An access token that is a JSON object is credential protection, and that file is not a sign-in. The model list is `GET {origin}/v3/config`, limited to the `cli` agent's ids. A failed read leaves the list unknown. An enterprise domain (`*.sso.copilot.tencent.com`, `*.copilot.qq.com`, or `*.sso.codebuddy.cn`, except `tencent.sso.copilot.tencent.com`) uses `https://{domain}` and only the models that host returns. A personal sign-in uses `https://copilot.tencent.com` or `https://www.workbuddy.ai`.

An API key, or a sign-in imported from the desktop file or from JSON with an access token and a refresh token, is kept in `workbuddy-auth.json` beside the config. A `ck_` or `pt_` key asks `/v3/config`. Another key is not asked, and its model list stays unknown. `POST /accounts/workbuddy` writes that file. The response does not include the key or the token.

## Calls

Another connected agent's model list is the whole catalog, including `login/<account>/<model>`. Its request comes back to the gateway.

Codex calls `https://chatgpt.com/backend-api/codex/responses` with the ChatGPT access token and `ChatGPT-Account-ID`. Within five minutes of expiry the token is refreshed at `https://auth.openai.com/oauth/token` with Codex's client id `app_EMoamEEZ73f0CkXaXp7hrann`, and the new tokens are written back to `~/.codex/auth.json`. Other fields in that file stay.

Claude does not send the sign-in to Anthropic. [login.RunClaude](../../internal/login/claude.go) starts the `claude` binary with `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, and `CLAUDE_CODE_OAUTH_TOKEN` removed from its environment. The first assistant message is returned to the caller. A tool call in that message is returned as a tool call, and the process is stopped before Claude Code runs the tool.

Copilot calls `https://api.individual.githubcopilot.com/chat/completions` with the exchanged session token. Gemini CLI and Antigravity call `https://cloudcode-pa.googleapis.com/v1internal:generateContent` with the access token and `project_id` from their own credential file.

A WorkBuddy desktop sign-in is read again for each call. The refresh token is not copied and the desktop file is not written. A saved sign-in keeps both tokens in `workbuddy-auth.json`. Within five minutes of expiry it is refreshed at `{origin}/v2/plugin/auth/token/refresh`, and the new tokens are written back to that file only. The call is `POST {origin}/v2/chat/completions` with `User-Agent` `WorkBuddy/5.9.3 WorkBuddy/5.9.3 CLI/2.165.0` on the China build and `WorkBuddy/5.9.3 workbuddy-ai/5.9.3 CLI/2.165.0` on the international build, `X-Product: SaaS`, and `X-IDE-Type: WorkBuddy`. An enterprise call also sends `X-Enterprise-Id` and `X-Tenant-Id`. An API key sends `X-API-Key` and `Authorization: Bearer` as that key, and `X-User-Id` `anonymous_` plus the key's last eight characters. The body is gzipped and `stream` is true. Importing a desktop sign-in refreshes it once into `workbuddy-auth.json` and does not write the desktop file.

## Quota reads

[login.Usage](../../internal/login/usage.go) asks each readable sign-in how much of its allowance is left, for the usage page's subscription cards. A plugin-occupied slot is not asked, a signed-out account is not listed, and a failed read is `unknown`, never zero. Nothing is written to the agent's files, and the token is not part of a quota row.

Codex with `auth_mode` `chatgpt` asks `GET https://chatgpt.com/backend-api/wham/usage` with the access token and `ChatGPT-Account-ID`, refreshing the token first exactly as a call would. The answer's `primary_window` and `secondary_window` are the plan windows: `used_percent` is the share spent, `reset_at` (or `reset_after_seconds`) is when the window renews, and a window of five hours is named `5h`, one of a week `week`. `credits` is the account's separately bought credits: the balance, or `unlimited`. An `apikey` sign-in has no plan windows and is not listed.

Claude is not asked over HTTP. [login.Usage](../../internal/login/usage.go) runs `claude -p /usage` and parses what Claude Code prints: `Current session` and `Current week` blocks, each with its percent used and its reset words. The reset stays the vendor's words; no clock time is invented.

Gemini CLI and Antigravity ask `POST {host}/v1internal:fetchAvailableModels` with the access token from their own credential file, where each model's `quotaInfo.remainingFraction` is the share of its quota still left and `resetTime` is when the window renews. Antigravity also asks `retrieveUserQuotaSummary`, which pools the models that share one allowance into its 5-hour and weekly buckets; those pooled windows are the card when they answer.

Verification adds `go test ./internal/login/`: `TestCodexUsageReadsWhamWindows` fails if a plan window or the credits balance is lost or the token appears in a quota row. `TestClaudeUsageParsesSessionAndWeek` fails if the printed session or week is misparsed. `TestAntigravityUsagePrefersThePooledSummary` fails if the per-model list replaces the pooled buckets. `TestUsagePluginSlotIsNotAsked` fails if a plugin-occupied account is dialed.

`GET /accounts` returns the rows. `PUT /accounts/discovery` with `{"on":false}` sets `noAccountDiscovery`.

Verification is `go test ./internal/login/ ./internal/gateway/ ./internal/console/`. `TestDiscoveryOffReadsNothing` fails if discovery off opens a credential file. `TestWorkBuddyDesktopLoginIsReadOnly` fails if a desktop call sends the refresh token, writes the desktop file, or offers a model that is not in that account's config. `TestWorkBuddyKeyIsNotCopiedIntoConfig` fails if the API key appears in the console response or in `config.json`. `TestCodexRefreshWritesBackAndListsOnlyBackendModels` fails if a hidden backend model is offered or the previous refresh token is left in `auth.json`. `TestAccountsAreNotCopiedIntoConfig` fails if the console response or config file contains the Claude token. `TestClaudeSubscriptionReturnsTheToolToTheCaller` fails if the tool call is not returned to the caller.
