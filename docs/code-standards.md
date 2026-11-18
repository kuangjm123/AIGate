# Code standards

A reviewer checks these rules. A PR that misses one gets a comment naming which.

## What a change contains

| Rule | What a reviewer looks at |
| --- | --- |
| A change does only what its goal needs. | Every behavior change has a reason. Unrelated refactors, extra features, and policy changes go in a PR of their own. |
| A fix covers exactly the cases it means to. | Which cases change, and which neighboring ones stay the same. Different errors are not handled as one. |
| A change holds on the whole path. | Follow a request from input to the response: auth, provider selection, fallback, usage, and any other side effect. One correct function does not make the operation correct. A value saved under one id is read under the same id. |
| A fix reaches every sibling of the bug. | The same pattern in another provider, route, plugin, or caller is fixed in the same change. That includes the other branches of the operation and the other messages that name the same rule. |
| A change re-derives everything built on what it changes. | When a formula, list, limit, or the timing of an async step changes, update every caller, saved copy, and test that assumed the old meaning, and run them. |
| A failed read, parse, or hash means "unknown". | It never means "empty", "none", or "equal". Keep the last good value, and set a bad file aside before writing over it. A list that guards access fails closed when its file cannot be read. Test with the file unreadable. |
| Another system's values come from that system. | When AIGate writes another agent's or provider's file, or speaks for it, take each field's type and value from that system's own data or docs, not only from field names. |
| Nothing that can block runs under a lock its reader needs. | Do not call anything that can block while holding a lock the waiter takes first. Set shared state before another task is allowed to read it. A change that starts concurrent work is run under a race detector before it is pushed. |
| A default never overrides a value the user set. | Before writing a setting, list everywhere it is read, and test with a user-set value present. A migration that rewrites a setting leaves a user's own value alone, or the release says why not. |
| Checks cover the edges the change touches. | For numbers, check the boundaries. For paths, check each platform. For concurrency, check races and stale state. |

## Tests

| Rule | What a reviewer looks at |
| --- | --- |
| A fix comes with a test that fails without it. | Put the old code back and run the test. It must fail. The PR says what it failed with. |
| A test never reaches the network. | A provider page or API is served from a local fake, built from bytes saved from the real one. A dependency comes from a lock file, not a registry's latest. |
| Tests use real inputs. | Fixtures look like real requests, real serialized output, and real files, including missing fields, empty values, and defaults. |
| Tests check behavior, not implementation. | Assert what the caller needs. A formatting or internal change should not break a test unless exact bytes are the requirement. |
| A test that fails on `main` is a bug to fix now. | "Fails the same on origin/main" is not a baseline. Fix it at its cause, or open an issue naming the cause, before shipping on top of it. |
| Tests stay off the real machine. | They use a temporary home and copies of config. They never read or write a live agent config, a real credential, or a running gateway. |
| Whatever a test starts ends when the test does. | A fake process, task, or refresh the test starts is stopped before the test returns. |
| A timing test leaves room on both sides. | Do not bound a test by a wall-clock limit a loaded runner can exceed. Reproduce a timing failure by injecting the delay. |

## Acceptance

A change is merged only when each of these is stated: what was run, and what was not run.

1. **A test that fails without the change.** Restore the old code, run the new test, and see a real failure that names the behavior. A compile error or a skipped test does not count. Restore the change and see the test pass.
2. **Tests away from live state.** No test reads or writes a live credential, a live agent config, or a running gateway.
3. **The build and the suite.** Format, build, and test the changed code and the checks that guard the rest of the repo. Run the suite, not only the package that looks touched.
4. **The real format where it can be checked.** A change to a provider, plugin, auth flow, or route is checked against that system's real reply or config format, using copies of credentials in a sandbox. If that check was not possible, say so. What was not tried is not described to users as working. A platform report is checked on that platform.

Merge only the commit that was reviewed. A push during the review is reviewed again before it merges. Write on the PR what was run and at which head, then merge.

## Reviewing a PR

- **Run it.** Pull the head, merge it onto current `main`, and run the acceptance checks. Reading the diff is not a review.
- **Break it on purpose.** Revert the fix, or break the code it guards, and check that the new tests catch each break.
- **Check the provider, not the PR's account of it.** Read the upstream source or docs for the behavior, and try the change against a real reply or a saved one.
- **Check each platform the change names.** An integer conversion, a path, or a connection error can differ by OS and architecture.
- **Follow the whole path.** A selection is saved and read under the same id. A control acts on the process that actually runs. Names used by one provider or agent do not collide with another's.
- **Classify narrowly.** An error class covers only the case it describes. A rate limit, an auth failure, and an empty balance are not one error.
- **A preview writes nothing.** A dry run or plan has no side effects.
- **Don't reverse a decision a maintainer has made.** Leave that PR for the maintainer.
- **Docs match the source.** A reference is checked against the code it describes, and every link resolves.
- **Keep the PR to its goal.** Unrelated tests and refactors go in a PR of their own. Comments name functions, not line numbers.

## Commits

A commit message names the area, then says what the user now sees, as plain behavior. Give the issue number or the reporter. The body says how it was verified, and which test fails without the change.

That record stays in the commit body. One commit has one goal. Two reports fixed together are two commits, so a revert takes back one and keeps the other. A fix found on the way goes in a commit of its own.

A commit's tree is `origin/main` plus its own change. A file the change never meant to touch is a stale tree. Stop, rebase onto `origin/main`, and do not commit files copied from an older checkout.
