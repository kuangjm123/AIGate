# Gateway

The gateway port accepts a model request from a coding agent, selects an account, calls that account's provider, and returns that provider's reply. The caller authenticates with a gateway token. The provider authenticates with the account credential. Those two secrets are different, and neither is accepted on the [console](console.md) port.

## Request path

1. Reject the request unless it presents a gateway token that is still valid.
2. Resolve the route for the requested model. An unknown model has no route and fails closed.
3. Take a snapshot of config. Later console writes do not change this attempt.
4. Run enabled plugins on that snapshot, in config order, before the upstream call.
5. Dial the first account on the route. The host dials the account's base URL. A plugin does not.
6. Stream the provider's reply to the caller.
7. On an eligible failure, try the next account on the route. Stop at the first reply that completes, or at the end of the list.
8. After each attempt finishes, append a usage record under the same route id, account id, and provider id used for that attempt.

Plugin hooks and the upstream dial run without holding the lock a config reader takes. The snapshot is published before those steps start.

## Errors

These classes stay separate. One handler does not collapse them.

| Class | Meaning |
| --- | --- |
| Auth failure | The gateway token is missing or unknown, or the provider rejected the account credential |
| Rate limit | The provider asked the gateway to slow down |
| Empty balance | The provider reported that the account has nothing left to spend |
| Upstream unavailable | The provider could not be reached, or closed the attempt before a completed reply |

A caller-side auth failure returns on that request. It does not try another account. A provider-side auth failure, a rate limit, and an empty balance return as that class. They fall through to the next account only when the route lists that class. The default eligible set is upstream unavailable alone.

## Provider migration

Migration moves an agent's model traffic onto the gateway. The console creates an account that holds the upstream credential, and a gateway token for the agent. The agent then uses the gateway listen address and that token.

The values written into an agent come from that agent's own configuration fields and docs. AIGate does not invent a field from its name.

Until the open decision below is closed, migration stops at showing the listen address and the new token. It does not edit the agent's files.

## Routing and fallback

A route has an id, a model match, and an ordered list of account ids. Accounts are tried in that order, one at a time. The attempt that completes is the reply the caller sees. Each attempt, including one that fails, writes its own usage record.

The route id, account id, and provider id saved for an attempt are the ids read when that attempt was chosen.

## Usage

An attempt record stores the route id, account id, provider id, the error class or success, and the start and finish. Token counts are copied from the provider's own usage object, with that provider's field names and meanings. A missing count stays unknown. It is not stored as zero.

The record is appended after the attempt finishes. A failed append does not change response bytes already sent. The console shows the gap. It does not show a zero in place of a missing store.

Usage is not written into the config file. The store is the one [state](state.md) names.

## Plugins

A plugin is named in config and is enabled or disabled there. A disabled plugin is not called. Enabled plugins run in config order on the snapshot for this attempt.

Before the dial, a plugin may read the request fields the host passes and replace those fields, or reject the request with an error class this page already lists. After the attempt, a plugin may read the response fields the host passes.

A plugin does not receive account credentials, gateway tokens, or the console credential. It does not choose the dial address, and it does not open its own connection. The host dials the selected account's base URL and no other.

## Open

- Which model dialects the gateway accepts. No dialect is implemented until this list names it.
- Which coding agents have a migration that edits their config. Until one is named, migration does not write agent files.
- How a plugin is loaded (in-process, or another host). The field and dial rules above hold either way.
