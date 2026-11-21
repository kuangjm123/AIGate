# Notes for coding agents

## Design

Open decisions that do not have source yet are in [design](docs/design/README.md).
Behavior this repository runs is indexed in [subsystems](docs/subsystems/README.md).
A design page is not a subsystem reference. The subsystem index lists only
behavior this repository already runs. An open decision on a design page stays
open until that page changes.

## Commands

Format, test, and build the module with:

    go fmt ./...
    go test ./...
    go test -race ./...
    go build -o bin/aigate ./cmd/aigate

When the control page source in `web/` changes, rebuild it before the Go build. The console embeds `internal/console/ui/dist`.

    cd web
    npm ci
    npm run build

## Subsystem design and review

Before changing a documented subsystem, read its
[design reference](docs/subsystems/README.md) and verify the linked source.
When preparing or reviewing a PR, follow the same page's documentation update,
semantic change description, and review rules.

## Code standards

Write and review changes against the [code standards](docs/code-standards.md).

## Community PRs

Before reviewing a community PR, read [contributors](docs/contributors.md):
what stops a review for possible harm, what is closed unreviewed, and the
authors whose PRs are closed on sight.
