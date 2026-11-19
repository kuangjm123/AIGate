# Console

The console listener is the control API. `GET /` returns the control page from [console.Server.page](../../internal/console/ui.go) before any session check, so the page can register and log in. The page is the React build in `internal/console/ui/dist`, produced from [web](../../web). `GET /assets/…` returns that build's script and style, also before a session check. The page calls only this port's JSON API. It does not accept a model request and does not call a provider. A model path is rejected by [console.Server.ServeHTTP](../../internal/console/server.go) and is not forwarded. `GET /favicon.ico` is an empty response.

`POST /register` and `POST /login` do not require a session. Registration is accepted only when the console's effective bind is loopback; a non-loopback bind returns `rejected`. A password shorter than 8 characters is refused. The password is stored as a salt and a hash. Login returns a session secret once. Later requests send `Authorization: Bearer` with that secret. [Config.SessionOK](../../internal/state/auth.go) does not accept a gateway token. `POST /logout` drops the current session. With no matching session, every other JSON route returns `unauthorized`. When the config cannot be read, `GET /config-status` does not require a session and returns `unreadable`.

Creating a gateway token returns the secret once, from `POST /tokens`. An optional `models` list limits what that token can see and call. An empty list does not. Later reads return [security.Mask](../../internal/security/security.go) and never the secret. A provider key is write-only: a read returns `credentialSet`, not the key. The create and replace responses do the same. An omitted key on update keeps the stored key. An empty key is rejected.

## Operations

Ids are assigned by the process. A delete that would leave a dangling id returns `in_use` and does not change the snapshot. After a successful write, the route's provider ids still resolve. [state.Validate](../../internal/state/validate.go) enforces that.

| Object | Read | Write |
| --- | --- | --- |
| User | None. A read does not return the password hash or session hashes | `POST /register` on a loopback console, `POST /login`, `POST /logout` |
| Provider | `GET /providers` returns id, protocol, base URL, models, model info, and whether a key is set | `POST /providers`, `PUT /providers/{id}`, `DELETE /providers/{id}` when no route still lists it. `info` omitted on update keeps the stored info |
| Route | `GET /routes` | `POST /routes`, `PUT /routes/{id}`, `DELETE /routes/{id}`. `providerIds` is the attempt order. An omitted `eligible` keeps the stored value on update and the default on create |
| Plugin | `GET /plugins` returns id, enabled, and order, sorted by order | `POST /plugins`, `PUT /plugins/{id}` sets enabled, `PUT /plugins/order` reorders. The row is not loaded |
| Gateway token | `GET /tokens` returns id, mask, revoked, and the model list when one was set | `POST /tokens` creates, `POST /tokens/{id}/revoke` revokes |
| Usage | `GET /usage` reads the store. Filters are `routeId`, `providerId`, `start`, and `end`. A record may include `model`, `session`, `tokenId`, `caller`, and `protocol` | None. `POST /usage` is not a write |
| Listen address | `GET /listeners` returns, for each port, the user-set address, the address in effect, and whether it is listening. It also returns `share` | `PUT /listeners`. A field left out stays as it was. An empty string is rejected, so this write cannot clear a user-set address. `share` sets whether a non-loopback gateway address may open |

`GET /usage` returns `{"status":"ok","records":[...]}` for a real read, including a missing file. It returns `{"status":"unknown"}` with no records and no total when the store cannot be read. The console does not append usage records.

`GET /listeners` leaves `effective` empty and `listening` false when that listener is closed. A non-loopback console without a credential is that case. The configured address is still reported.

`GET /config-status` returns `{"config":"ok"}` or `{"config":"unreadable"}`. While the snapshot is nil, every other JSON route returns `config_unreadable`. `GET /` still returns the page.

Listen addresses, routes, and plugin order are the values the gateway reads from the snapshot after [App.Update](../../internal/app/app.go). The write does not send an HTTP call to the gateway port.

Logs are [security.AccessLine](../../internal/security/security.go). They do not include the bearer token or the body.

Verification is the command list in [AGENTS.md](../../AGENTS.md).
