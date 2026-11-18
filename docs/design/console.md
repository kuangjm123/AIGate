# Console

The console port is the control surface. It serves the control UI and the JSON API that UI calls. It does not accept a model request, and it does not call a provider on behalf of one.

A console session uses the console credential. A gateway token does not open the console. The console credential does not authorize a model request. See [security](security.md) for when that credential is required.

Creating a gateway token returns the secret once. Later reads return a mask. Account credentials are write-only: a read returns whether one is set, not the secret.

## Operations

| Object | Read | Write |
| --- | --- | --- |
| Provider | Id, protocol, base URL, model list | Create, update, delete when no account still references it |
| Account | Id, provider id, mask of the credential | Create, replace the credential, delete when no route still lists it |
| Route | Id, model match, account order, eligible error classes | Create, update, delete |
| Plugin | Id, enabled flag, order | Enable, disable, reorder |
| Gateway token | Id, mask | Create, revoke |
| Usage | Attempt records by route id, account id, provider id, and time | None |
| Listen address | The address in effect for each port | Set. A user-set address is left in place unless this write is the user setting it |

A delete that would leave a dangling id is rejected. The account's provider id and the route's account ids still resolve after a successful write.

Usage reads the store [state](state.md) describes. The console does not append usage records.

Listen addresses, routes, and plugin order are the same values the gateway reads. The write updates the shared snapshot. It does not send an HTTP call to the gateway port.

## Open

The layout of the control UI is open. Whatever is shown, it calls only this port's JSON API, and it follows the read and write columns above.
