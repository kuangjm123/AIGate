# Security

The gateway and the console are different trust boundaries on different ports. A secret from one is not a credential for the other, and no response body carries a provider credential, a gateway token secret, or the console credential, except the one-time create response [console](console.md) defines.

## Bind

Both listeners default to loopback. See [ports](ports.md).

The console listener opens on a non-loopback address only when a console credential is set. Otherwise that listener does not open, and the gateway listener is unaffected. Loopback console with no console credential still serves only the local machine.

The gateway listener requires a valid gateway token on every model request, including on loopback. There is no anonymous model call.

## Responses and logs

A model response carries the provider's reply and the error class. It does not carry account credentials, other accounts, gateway token secrets, or the console credential.

A console response masks secrets. Logs on both ports omit the same fields.

The process does not send telemetry, and it does not add a relay, reseller, or affiliate address.

## Plugins

A plugin sees the request and response fields the host passes. It does not receive secrets. It cannot add an outbound address. The only dial is the one the host makes to the selected account's base URL, as [gateway](gateway.md) describes.

## Provider base URLs

The host dials the account's configured base URL and does not follow a redirect to a different origin.

The console port's origin is never a legal provider base URL. A base URL that targets a link-local address is rejected.

A loopback base URL is legal only when that account's saved origin is exactly the origin dialed. Local models stay possible. The console origin stays illegal. An unset or unreadable base URL is unknown and is not dialed.

## Open

Whether a non-loopback gateway bind needs an extra confirmation, beyond the gateway token on each request, is open. The token requirement above already holds.
