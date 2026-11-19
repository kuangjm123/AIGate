# Security

The gateway and the console are different trust boundaries on different ports. A secret from one is not a credential for the other. No response body carries a provider key, a gateway token secret, a password, or a session secret, except the one-time `POST /tokens` body and the one-time `POST /login` body.

## Bind

Both listeners default to loopback. See [ports](ports.md).

[security.ConsoleMayListen](../../internal/security/security.go) opens the console on a non-loopback address only when at least one user exists. Otherwise that listener does not open, and the gateway listener is unaffected. Loopback console with no user still serves the local machine so the first registration can happen there. `localhost`, `127.0.0.1`, and `::1` count as loopback. The check does not resolve DNS. Registration itself is accepted only while the console's effective bind is loopback.

A model request from a remote peer, or any request that carries `X-Forwarded-For` or `Forwarded`, needs an issued gateway token. A missing or unknown token is `auth_failure`. On a loopback peer with neither of those headers, any non-empty bearer is accepted. `magpie` and `magpie-<name>` are recorded as the caller. Any other local bearer is not stored; the usage line records the first word of `x-aigate-agent`, or of `User-Agent` when that header is absent. An empty bearer is still `auth_failure`. A console session is not a gateway token for a remote caller.

[security.GatewayMayListen](../../internal/security/security.go) opens a non-loopback gateway address only when `share` is true. The configured address is kept. Loopback opens either way.

## Responses and logs

A model response from the current process is `config_unreadable`, `auth_failure`, `no_dialect`, or a port rejection. It does not carry provider keys, gateway token secrets, passwords, or session secrets.

A console response masks secrets, as [console](console.md) describes. [security.AccessLine](../../internal/security/security.go) is the only request line written: method, path, and status. The query, the `Authorization` header, and the body are not part of it.

The process does not send telemetry, and it does not add a relay, reseller, or affiliate address. No code path dials an address of its own.

## Plugins

A [gateway.View](../../internal/gateway/run.go) passed to a hook has no provider key, gateway token, or session secret. The running server does not call hooks. A hook cannot add an outbound address. The only dial is the one a host [Dialer](../../internal/gateway/run.go) would make to the selected provider's base URL.

## Provider base URLs

[security.CheckBaseURL](../../internal/security/security.go) is the decision, and it does not dial or resolve a name.

- An empty or unparseable URL is unknown and is not dialed. An empty URL may be stored. A non-empty URL that does not parse is rejected on write.
- The console listener's origin is not a legal provider base URL. With the console on loopback, another loopback name on that same port is the same listener. An unspecified console address (`0.0.0.0` or `::`) makes every base URL on that port illegal.
- An IP literal that is link-local is rejected. Name hosts are not resolved, so a name that would point at a link-local address is not detected here.
- A loopback base URL is legal only when the saved origin and the origin to be dialed are the same. When the dial origin is omitted, the saved origin is the one that would be dialed.

[security.AllowRedirect](../../internal/security/security.go) is false when the next URL has a different origin. The process has no dialer, so it does not follow redirects.

Verification is the command list in [AGENTS.md](../../AGENTS.md).
