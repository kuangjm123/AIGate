# Community PRs

Two checks come before a review of what a change does. A PR that could harm users is caught first, and an author who keeps sending work that costs more to review than it gives is stopped.

## Look for harm first

Before running a PR, read its diff for anything that reaches past its stated goal. Any one of these stops the review until a maintainer has looked:

- It changes CI workflows, release or publish scripts, or repository permissions and secrets.
- It adds a network address, an upload, telemetry, or sends a credential, token, cookie, or file anywhere it did not go before.
- It reads or writes a credential, key, or agent config the goal does not need.
- It adds a dependency, an install script, a binary, a minified or encoded blob, or code built from a string at run time.
- It adds a relay, reseller, or affiliate address, or puts back a provider or vendor a maintainer removed.

Name the line that stopped the review. Do not run the PR on a machine with real accounts until a maintainer has cleared it.

## Close without a full review

Close with a short reason, without running it, a PR that:

- changes a decision a maintainer has already made (see [code standards](code-standards.md));
- resubmits a PR closed for its idea, with nothing new that answers the reason;
- is one of several near-identical PRs opened seconds apart.

## Credit what a reporter gave

Whoever uses a reporter's work credits it:

- A fix branch on the reporter's fork is merged with their commits as they are. The harm check above applies first. Anything that still has to change goes in a commit of our own, and the issue says what changed and why.
- A fix that uses the reporter's patch, test, request body, error text, or root cause carries `Co-authored-by: Name <email>`. Use their GitHub noreply address when no other address is known.
- A report that only says something is broken is named in the commit body (`#<n>, <login>`), with no trailer.

## Blocking an author

Propose a block when any one of these holds:

1. Three or more PRs closed as low-value (wrong, untested, unrelated, or filler), and fewer than 30% of their PRs merged.
2. A PR resubmitted after an explicit rejection, twice.
3. Anything deceptive: a change hidden outside the stated goal, a faked test or verification, or a harm-check item done on purpose.
4. Low-value PRs opened in a burst, several within a minute.
5. A removed provider, relay, or advertisement put back after a no.

An agent only proposes. It writes the evidence (PR numbers, dates, what was wrong) to a maintainer; the maintainer decides. Close a blocked author's new PRs and issues with: "Thanks — this account's contributions aren't accepted in this repository."

One or two low-value PRs, with no pattern yet, still get a normal review. Note them if the next one is low-value too.

## Lists

Blocked: none.

Watched: none. A watched author is not blocked. The review checks the gaps named for them before merging.

Extra care: none. An author a maintainer kept off the block list is still reviewed: every line of the diff is read, the harm check is run in full, and the PR is run on current `main` rather than trusted from its description or CI.
