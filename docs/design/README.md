# Design

These pages record the boundaries of AIGate before this repository has source to verify. They are proposals. A [subsystem reference](../subsystems/README.md) is the page that describes behavior the source already has. When a fact moves there, this set links to that reference and drops the copy.

AIGate is one process with two listeners. The gateway port takes model requests from coding agents. The console port configures the gateway and reads what it recorded. The console does not forward model traffic. The gateway does not serve the console.

| Page | What it settles |
| --- | --- |
| [Ports](ports.md) | The two listeners, what each carries, and how a change of config reaches the gateway |
| [Gateway](gateway.md) | The request path, provider migration, routing and fallback, usage, and plugins |
| [Console](console.md) | What the control port reads and writes, and its own login |
| [State](state.md) | Shared ids, the config file, the usage store, and a failed read |
| [Security](security.md) | Bind addresses, credentials in responses, plugins, and provider base URLs |

No build, test, or format command exists yet. Record the exact commands in [AGENTS.md](../../AGENTS.md) when the first package lands. Until then a change does not add a toolchain of its own.

An open decision stays on the page that owns it. An implementation does not pick a value for an open decision inside an unrelated change.
