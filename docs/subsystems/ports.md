# Ports

One process opens the gateway listener and the console listener. They share the in-memory config snapshot and the usage store. Choosing a provider does not call the console over HTTP. Applying a config change does not call the gateway over HTTP. [App.Update](../../internal/app/app.go) writes the snapshot, then rebinds a listener in this process when its address changed.

| Listener | Default, in memory only | Carries | Does not carry |
| --- | --- | --- | --- |
| Gateway | `127.0.0.1:3425` | Model requests, `GET /api/hello` | Console login, config writes, usage queries, plugin enablement |
| Console | `127.0.0.1:8081` | The control page and the JSON API | Model completion, streaming, or any other model call |

[security.EffectiveListen](../../internal/security/security.go) applies a default only when that listener's config field is empty. The default is not written back to the config file.

[App.Start](../../internal/app/app.go) compares the two addresses with [security.SameListen](../../internal/security/security.go). The same host and port returns [ErrSameListen](../../internal/app/app.go): neither listener opens, and the config file is left as it was. Names are not resolved, so `localhost` and `127.0.0.1` are not treated as the same host.

[console.IsModelPath](../../internal/console/path.go) reports `/v1` and `/v1/…`, `/v1beta` and `/v1beta/…`, `/api/chat`, `/api/tags`, and `/api/hello`. Those requests are rejected on the console and are not forwarded. `GET /api/hello` on the gateway names this process and does not require a token. [console.IsConsolePath](../../internal/console/path.go) reports a control route. Those requests are rejected on the gateway and are not applied. [gateway.Server.ServeHTTP](../../internal/gateway/server.go) performs that rejection before it reads a token.

The console opens on a non-loopback address only when at least one user exists. Otherwise that listener stays closed and the gateway still opens. A non-loopback gateway address opens only when [Config.Share](../../internal/state/config.go) is true. Otherwise that listener stays closed and the console still opens. See [security](security.md).

Verification is the command list in [AGENTS.md](../../AGENTS.md).

The dialects the gateway accepts are listed in [gateway](gateway.md). The port split rejects a model path on the console and does not parse the body.
