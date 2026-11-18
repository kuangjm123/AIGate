# State

The console writes config. The gateway reads config. Usage is a second store. Both sides use the same ids for a provider, an account, a route, and the usage of an attempt.

An id is an opaque string, assigned when the object is created, unique among its type, and not reused. A display name can change. The id does not.

## Files

| Store | Path | Writer | Reader |
| --- | --- | --- | --- |
| Config | `AIGATE_CONFIG` if set, otherwise `aigate/config.json` under the platform's per-user config directory | Console | Gateway and console |
| Usage | `aigate/usage/` beside that config directory | Gateway, append only | Console |

`AIGATE_CONFIG` is a user-set path. When it is set, the default path is not consulted and not written.

Config holds listeners, providers, accounts, routes, plugins, gateway tokens, and the console credential. Usage holds attempt records. A usage write does not rewrite the config file.

## Failed read

A failed read, parse, or hash means "unknown". It never means "empty", "none", or "equal". The process keeps the last good config in memory and sets the bad file aside before a later write replaces it. A list that guards access fails closed when its file cannot be read: with no good snapshot yet, the gateway accepts no model request and the console rejects every route except the one that reports the config is unreadable.

A successful read of an empty provider list is a real empty list. New model requests fail closed because there is no account to dial. That is not the same outcome as an unreadable file, and the console shows it as empty.

A failed read of the usage store does not change config and does not report totals as zero. The console shows the usage store as unknown.

## Snapshot

A console write validates the next config, writes it to a temporary file, then replaces the config file. The gateway's in-memory snapshot swaps to that config only after the replace finishes. An attempt already running keeps the snapshot it took at the start. See [gateway](gateway.md).

The default listen addresses in [ports](ports.md) apply only to a listener the config does not set. A write of defaults does not replace an address the user has set.

## Secret fields

These fields are secret: account credentials, gateway token secrets, and the console credential. The config file holds them. API responses follow [console](console.md): a secret is returned once at create, or only as a mask. Logs do not print them.

## Open

Encryption of the config file at rest is open. Until it is decided, the file is protected by the user account's file permissions on that platform.
