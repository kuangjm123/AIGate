# Ports

AIGate opens two TCP listeners in one process. They share config and usage state in memory. Choosing a provider does not call the console over HTTP. Applying a config change does not call the gateway over HTTP. The console updates the shared snapshot. The gateway reads that snapshot.

| Listener | Default | Carries | Does not carry |
| --- | --- | --- | --- |
| Gateway | `127.0.0.1:8080` | Model requests from coding agents | Console login, config writes, usage queries, plugin enablement |
| Console | `127.0.0.1:8081` | The control UI, and the JSON API that UI calls | Model completion, streaming, or any other model call |

A user-set listen address is kept. The defaults above apply only when that listener has no user-set address. See [state](state.md).

The two listen addresses are different. The same host and port for both is a failed start: the process opens neither listener and leaves the last good config on disk untouched.

The console UI and its JSON API are both on the console port. The UI changes state only by calling that API. A model request that arrives on the console port is rejected and is not forwarded. A console route that arrives on the gateway port is rejected and is not applied.

## Open

The inbound model dialects the gateway accepts are open. They are listed on [gateway](gateway.md). The port split does not depend on that list.
