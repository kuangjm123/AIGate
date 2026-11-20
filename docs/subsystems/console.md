# Console

The console listener is the control API. `GET /` returns the control page from [console.Server.page](../../internal/console/ui.go) before any session check, so the page can register and log in. The page is the React build in `internal/console/ui/dist`, produced from [web](../../web). `GET /assets/…` returns that build's script and style, also before a session check. The page calls only this port's JSON API. It does not accept a model request. A model path is rejected by [console.Server.ServeHTTP](../../internal/console/server.go) and is not forwarded. `POST /providers/{id}/test` checks that saved provider with one short request on its first conversation model. `POST /providers/detect` reads the base URL and names the protocol that answers. Ollama and Gemini are recognized from their own catalog paths. Chat and Anthropic share `/v1/models`; an OpenAI list (`object` `list`, or a model with `owned_by`) is chat, and an Anthropic model object is anthropic. `POST /providers/models` reads that protocol's model catalog. `POST /providers/check` sends one short request per conversation model, up to 24. These reads do not return a key, do not change config, and do not append a usage record. An empty protocol is tried as chat. A provider name is the label; the id is still assigned by the process and does not change. Creating a provider requires the name and at least one key. An update that omits keys keeps the stored keys. `addCredentials` appends keys. `DELETE /providers/{id}/keys/{index}` removes one key and refuses to remove the last. `GET /favicon.ico` is an empty response. `GET /agents` and the connect and disconnect routes are the Agent page. They are session routes. The page does not send a model request.

`POST /register` and `POST /login` do not require a session. Registration is accepted only when the console's effective bind is loopback; a non-loopback bind returns `rejected`. A password shorter than 8 characters is refused. The password is stored as a salt and a hash. Login returns a session secret once. Later requests send `Authorization: Bearer` with that secret. [Config.SessionOK](../../internal/state/auth.go) does not accept a gateway token. `POST /logout` drops the current session. With no matching session, every other JSON route returns `unauthorized`. When the config cannot be read, `GET /config-status` does not require a session and returns `unreadable`.

Creating a gateway token returns the secret once, from `POST /tokens`. An optional `models` list limits what that token can see and call. An empty list does not. Later reads return [security.Mask](../../internal/security/security.go) and never the secret. A provider key is write-only: a read returns `credentialSet`, not the key. The create and replace responses do the same. An omitted key on update keeps the stored key. An empty key is rejected.

## Operations

Ids are assigned by the process. A delete that would leave a dangling id returns `in_use` and does not change the snapshot. After a successful write, the route's provider ids still resolve. [state.Validate](../../internal/state/validate.go) enforces that.

| Object | Read | Write |
| --- | --- | --- |
| User | None. A read does not return the password hash or session hashes | `POST /register` on a loopback console, `POST /login`, `POST /logout` |
| Provider | `GET /providers` returns id, name, protocol, base URL, models, model info, whether a key is set, and the key count | `POST /providers`, `PUT /providers/{id}`, `DELETE /providers/{id}` when no route still lists it. `info` omitted on update keeps the stored info. Keys omitted on update stay. `POST /providers/detect`, `POST /providers/models`, and `POST /providers/check` dial the address in the body, or the saved provider when `id` is set and a field is left empty. `POST /providers/{id}/test` dials the saved provider |
| Route | `GET /routes` | `POST /routes`, `PUT /routes/{id}`, `DELETE /routes/{id}`. `providerIds` is the attempt order. An omitted `eligible` keeps the stored value on update and the default on create |
| Plugin | `GET /plugins` returns id, enabled, and order, sorted by order | `POST /plugins`, `PUT /plugins/{id}` sets enabled, `PUT /plugins/order` reorders. The row is not loaded |
| Agent | `GET /agents` returns `status` `ok` and every known coding agent. `state` is `absent`, `off`, `on`, `drift`, `hand`, `unknown`, or `manual`. A read does not return a credential that was in the agent file. `status` `unknown` with no agents means this computer's home directory could not be read | `POST /agents/{id}/connect` and `POST /agents/{id}/disconnect`. Connect writes that agent's own config. Disconnect writes the recorded previous values back. Refusals are `no_models`, `not_listening`, `unknown`, `absent`, `unsupported`, `not_connected`, and `id_taken`, and they do not write. An unknown id is `not_found`. The files and the per-agent fields are [agent wiring](agent-wiring.md) |
| Gateway token | `GET /tokens` returns id, mask, revoked, and the model list when one was set | `POST /tokens` creates, `POST /tokens/{id}/revoke` revokes |
| Usage | `GET /usage` reads the store. Filters are `routeId`, `providerId`, `start`, and `end`. A record may include `model`, `session`, `tokenId`, `caller`, and `protocol` | None. `POST /usage` is not a write |
| Conversation | `GET /conversations` lists dialogues that have a client session id. `GET /conversations/turns?session=` returns each finished call in order. `output` is the whole reply, with a stream already joined | None. The gateway appends a turn when a reply finishes |
| Listen address | `GET /listeners` returns, for each port, the user-set address, the address in effect, and whether it is listening. It also returns `share` | `PUT /listeners`. A field left out stays as it was. An empty string is rejected, so this write cannot clear a user-set address. `share` sets whether a non-loopback gateway address may open |

`GET /usage` returns `{"status":"ok","records":[...]}` for a real read, including a missing file. It returns `{"status":"unknown"}` with no records and no total when the store cannot be read. The console does not append usage records.

`GET /conversations` and `GET /conversations/turns` use the same `status` pair. A missing dialogue is `ok` with an empty turn list. An unreadable dialogue file is `unknown`, which is not an empty dialogue.

`GET /listeners` leaves `effective` empty and `listening` false when that listener is closed. A non-loopback console without a credential is that case. The configured address is still reported.

`GET /config-status` returns `{"config":"ok"}` or `{"config":"unreadable"}`. While the snapshot is nil, every other JSON route returns `config_unreadable`. `GET /` still returns the page.

Listen addresses, routes, and plugin order are the values the gateway reads from the snapshot after [App.Update](../../internal/app/app.go). The write does not send an HTTP call to the gateway port.

Logs are [security.AccessLine](../../internal/security/security.go). They do not include the bearer token or the body.

Verification is the command list in [AGENTS.md](../../AGENTS.md).
