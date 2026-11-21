# State

The console writes config. The gateway reads the published snapshot. Usage is a second store. Both sides use the same id for a provider, a route, and the usage of an attempt.

An id from [state.NewID](../../internal/state/id.go) is 128 random bits, hex encoded, unique among its type, and not drawn from a recycle pool. A display name is not the id. The id does not change when the object is updated.

## Files

[state.ResolvePaths](../../internal/state/paths.go) chooses both locations.

| Store | Path | Writer | Reader |
| --- | --- | --- | --- |
| Config | `AIGATE_CONFIG` when it is set, otherwise `aigate/config.json` under the platform per-user config directory | Console, through [Store.Update](../../internal/state/store.go) | Gateway and console, through [Store.Current](../../internal/state/store.go) |
| Usage | `usage/` in the directory that contains the config file | Gateway attempt loop, append only | Console `GET /usage` |

`{UserConfigDir}/aigate/config.json` and `{UserConfigDir}/aigate/usage/` are the default pair. When `AIGATE_CONFIG` is `D:\cfg\gate.json`, usage is `D:\cfg\usage\` and the default file is not read or written. The usage lines are `usage/attempts.jsonl`.

The config file is JSON. It holds listeners, the share flag, providers, routes, plugins, gateway tokens, and console users. Notes for a connected coding agent are not in this document. They are `agents/<id>.json` beside the config file. See [agent wiring](agent-wiring.md). A provider stores a display name, one protocol, and one or more upstream keys for that protocol. `credential` remains the first key. `credentials` is the full list. Optional [ModelInfo](../../internal/state/config.go) for a listed model carries display name, kind, hidden, context, reasoning, and whether it accepts images. A gateway token may store a model allow-list. A user stores a password salt, a password hash, and session hashes. A usage write does not rewrite it. Directories are created at mode `0700` and the files at mode `0600` when the platform honors those bits. The file is not encrypted. Encryption at rest is open; see [state design](../design/state.md).

Empty listen fields are stored as empty. They are not filled with `127.0.0.1:3425` or `127.0.0.1:8081`. `share` false does not rewrite a non-loopback gateway address; that listener stays closed until share is true.

A route field `eligible` that is absent from the JSON uses only `upstream_unavailable` at attempt time. A present empty array stays empty and is written back as `[]`. [Route.EligibleOrDefault](../../internal/state/config.go) is that distinction. [state.Validate](../../internal/state/validate.go) rejects a dangling provider id, a provider with no key, an unknown class, a base URL [security.CheckBaseURL](../../internal/security/security.go) does not allow, and two listeners that bind the same host and port. A route lists provider ids in attempt order.

## Failed read

[Store.Load](../../internal/state/store.go) publishes an empty config when the file is missing, and does not create the file. A successful document with an empty provider list is that empty list.

A failed read, parse, or content hash is not an empty config. A document that still contains `accounts`, `consoleCredential`, or a route `accountIds` field is the same failure: those fields are not dropped and the rest is not published. The bad file is renamed to `<file>.bad`, or `<file>.bad.<n>` when that name is taken. The last good snapshot stays in memory. The first failed load leaves [Store.Current](../../internal/state/store.go) nil: the gateway accepts no model request, and the console can still serve `GET /` and `GET /config-status`. A later process start finds no file at the config path and publishes an empty config. The bad bytes stay in the `.bad` file.

A failed hash of a new document aborts [Store.Update](../../internal/state/store.go) before the file or the snapshot changes.

## Snapshot

[Store.Update](../../internal/state/store.go) clones the current config, applies the change, validates, writes a temporary file in the same directory, and replaces the config file. The in-memory snapshot pointer swaps only after that replace. [Store.Current](../../internal/state/store.go) returns a deep copy. An attempt keeps the copy it took at the start; a later update does not change it.

The reader lock is held only while that copy is made. The file write does not hold it. Plugin hooks and a dial do not hold it.

## Usage

An [AttemptRecord](../../internal/state/usage.go) stores the route id and provider id chosen for that attempt, the outcome, and the start and finish. `usage` is the provider's own object. When it is missing, the field is omitted. It is not stored as zero. A dialect attempt may also store `model`, `session`, `tokenId`, `caller`, and `protocol`. `tokenId` is the gateway token's id, not its secret. `caller` is that id for an issued token, `magpie`, `magpie-<name>`, `aigate`, or `aigate-<name>` for a local key of that form, or the first word of the client identifier for any other local key. The bearer secret is not stored. An older line that lacks those fields, or that still has `accountId`, still parses; `accountId` is ignored.

[Store.ReadUsage](../../internal/state/usage.go) returns an empty list when the file is missing. A failed read, or a line that does not parse, returns [ErrUsageUnknown](../../internal/state/errors.go). That error does not change config and is not a zero total.

A finished model call that brought a session id is also filed under that id by [Store.AppendTurn](../../internal/state/conversation.go). The file is `conversations/<sha256 of the session>.jsonl` inside the usage directory. Each line is one call. `output` is the whole reply; a stream is joined before the line is written. `input` is the last user text in that request. A call with no session id is not a line. A missing directory is an empty list. A file that cannot be parsed returns [ErrConversationUnknown](../../internal/state/errors.go), which is not an empty dialogue.

## Secret fields

Provider keys and gateway token secrets are stored in the config file. A console password is stored only as a salt and a hash from [security.HashPassword](../../internal/security/security.go). A session is stored only as [security.SecretHash](../../internal/security/security.go). API responses follow [console](console.md). Logs use [security.AccessLine](../../internal/security/security.go), which is the method, the path, and the status.

Verification is the command list in [AGENTS.md](../../AGENTS.md).
