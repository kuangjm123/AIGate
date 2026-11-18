# Subsystem design references

These pages describe current responsibilities, runtime paths, state, and contracts. Start with the subsystem relevant to your task, then follow its source links to verify the implementation you change.

Nothing is listed yet. This repository has no implementation, so there is no reference beside this page and no source to verify. The [README](../../README.md) states the product. It does not name subsystems, and it is not permission to add pages for provider migration, plugin hosting, auth, usage, or routing.

Add a row only when a reference describes behavior that source in this repository already has, and the page links to that source. Until the first such page exists, this index has no table. A proposal for behavior that does not exist yet is written in [design](../design/README.md), not on this index.

A subsystem can span several packages or repositories. Its reference describes the behavior those parts provide together; it does not need to list every function.

## Maintain a reference

Update the owning reference in the same PR when a change alters a documented responsibility, interface, state transition, runtime path, compatibility rule, or failure behavior. An internal refactor that preserves these contracts does not require a prose change.

Keep each fact in one reference and link to it from agent instructions or other pages. Link to source files and named symbols rather than copying implementation details that change frequently. Explain field meanings and constraints that signatures alone do not express.

For a new reference, include the responsibilities and boundaries, source of truth, main runtime paths, state transitions, important constraints, and existing verification commands. Include failure and compatibility behavior where relevant. Document current behavior separately from proposed changes; the proposal belongs in its issue or PR. Add the page to a table on this index only in the same change that adds the source it describes.

## Describe a PR's semantic changes

For each affected subsystem, explain the observable change with its trigger and before/after behavior. Include changes to interfaces, state, compatibility, and failure behavior when relevant. Link the affected reference, implementation, and verification evidence. State when a refactor preserves the documented behavior.

Name the input and the condition that trigger the change, the behavior a caller sees before and after, and the test that exercises that transition.

This is an author-provided explanation that a reviewer verifies against the diff and surrounding code. It is not an automatically proven compatibility verdict.

With no subsystem listed, a PR has no reference to update. Say that, and do not describe planned behavior as current.

## Review a change

1. Identify the affected subsystem and the actual runtime path, including which component owns the behavior. If this index lists nothing that matches, the change is adding a subsystem: the reference and the source arrive together, or the PR stops.
2. Compare the claimed before/after behavior with the base and head implementation. Trace relevant callers and state transitions beyond the changed lines.
3. Check that changed contracts appear in the owning reference and that source links still resolve.
4. Check that the cited tests or runtime evidence exercise the claimed behavior. Report checks that were not run.

A documentation update alone does not prove the implementation. Existing tests and runtime evidence remain necessary for behavior changes.
