# Design

These pages record decisions this repository has not implemented. A [subsystem reference](../subsystems/README.md) describes behavior the source already has. When a fact moves there, this set links to that reference and drops the copy.

| Page | What is still open |
| --- | --- |
| [Ports](ports.md) | Nothing of the port split is still open |
| [Gateway](gateway.md) | How a plugin is loaded |
| [Reasoning effort](reasoning-effort.md) | Nothing of the level mapping is still open |
| [State](state.md) | Encryption of the config file at rest |
| [Security](security.md) | Nothing of the non-loopback gateway confirmation is still open |
| [Usage ledger](usage-ledger.md) | Disk block cache, provider rename mapping, cache-hit/decode summaries, and OTLP export |
| [Routing parity](routing-parity.md) | Intent rules, slow start, per-request decision records, and a switchable strategy registry remain open; steps 1–5 are implemented |
| [Routing strategies](routing-strategies.md) | Per-request exact key promotion for `Exact` pools remains open; the strategy algorithms are implemented |

Commands for the module are in [AGENTS.md](../../AGENTS.md).

An open decision stays on the page that owns it. An implementation does not pick a value for an open decision inside an unrelated change.
