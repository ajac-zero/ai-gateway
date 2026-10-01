---
name: ai-gateway-patch-workflow
description: Use when changing, adding, rebasing, publishing, retiring, or deploying fork patches in this Envoy AI Gateway repository. Covers jj patch/* bookmarks, upstream PR heads, fork/main, fork-refresh, conflict resolution, and upstream synchronization.
---

# AI Gateway Patch Workflow

Use `jj`, not Git, for version-control mutations in this colocated repository. Git commands are acceptable for read-only inspection.

## Repository Model

- `trunk()` is exactly `main@upstream`.
- Every independently upstreamable change is rooted at `trunk()` and selected for the fork branch by a local `patch/*` bookmark.
- `fork/main` is the fork's combined branch: a generated multi-parent merge of `fork_parents()` (upstream plus every `patch/*`).
- A GitHub PR may require a separate legacy-named bookmark pointing to the same commit as its `patch/*` bookmark.
- Conflict resolutions between otherwise independent patches belong in `fork/main`, not in either patch.
- Never develop directly in `fork/main` except to resolve integration-only conflicts.

The repository-scoped jj config is versioned as `jj-repo-config.toml` next to this skill. In a new clone, run `scripts/fork-bootstrap` once; it colocates jj, adds the `upstream` remote, fetches, tracks the fork's bookmarks, and installs the config. Orbs run it from `.agents/setup`. The config provides:

```text
trunk()
fork_patches()
fork_parents()
fork_head()

jj patch-new
jj fork-refresh
jj fork-sync
jj fork-log
jj fork-parents
jj fork-conflicts
jj fork-stat
```

## Before Any Patch Work

Inspect the graph and working copy:

```bash
jj status
jj log -r 'trunk() | fork_head() | fork_patches()'
```

Do not modify unrelated changes. If `@` is `fork/main`, start patch work with `jj new <patch-bookmark>` or `jj new trunk()` rather than editing `@`.

## Fix an Existing Patch

For example, to fix `patch/native-gemini-ingress`:

```bash
jj new patch/native-gemini-ingress
jj describe -m "fix: describe the bug"
```

Edit and test the code. Then advance the selector:

```bash
jj bookmark set patch/native-gemini-ingress -r @
jj fork-refresh
jj fork-conflicts
```

If the patch has a separate PR-head bookmark, advance that bookmark to the same tip too:

```bash
jj bookmark set <pr-head-bookmark> -r patch/native-gemini-ingress
```

Verify the patch remains based only on upstream:

```bash
git merge-base upstream/main patch/native-gemini-ingress
git rev-list --left-right --count upstream/main...patch/native-gemini-ingress
git diff --stat upstream/main...patch/native-gemini-ingress
```

The left count should normally be `0` after synchronization.

## Create a New Patch

Start from upstream, never from `fork/main`:

```bash
jj patch-new -m "fix: describe the change"
```

Edit and test, then create the fork selector:

```bash
jj bookmark create patch/<short-name> -r @
jj fork-refresh
jj fork-conflicts
```

Only create a separate PR-head bookmark when publishing upstream requires a different branch name:

```bash
jj bookmark create <github-pr-head> -r patch/<short-name>
```

## Update an Existing Change In Place

Prefer a follow-up commit with `jj new <patch>` for normal fixes. If explicitly asked to amend an existing unpublished change:

```bash
jj edit <change-id>
```

After editing, jj automatically rebases descendants. Still run:

```bash
jj fork-refresh
```

Do not rewrite a published PR change without expecting a non-fast-forward bookmark update.

## Automated Updates

`scripts/fork-update` (run from the repository root) handles the routine path:

```bash
.agents/skills/ai-gateway-patch-workflow/scripts/fork-update check     # read-only report
.agents/skills/ai-gateway-patch-workflow/scripts/fork-update apply --push
.agents/skills/ai-gateway-patch-workflow/scripts/fork-update assemble --push
```

`check` replays each stale patch onto `main@upstream` in a throwaway worktree, then builds, vets, and tests the packages it touches. It reports each patch as `up-to-date`, `clean`, `conflict`, or `broken`. It exits 0 when nothing is stale, 10 when every stale patch is clean, and 20 when an agent is needed. `apply` mutates nothing unless every stale patch is clean. It then rebases them with jj, verifies that each jj result has the same tree as the checked replay, fast-forwards `main`, and assembles `fork/main`. `assemble` moves `fork/main` only after a conflict-free merge passes build, vet, lint, generated-file, and unit-test checks. `--push` pushes `main`, every `patch/*`, and `fork/main` by name. Check logs are written under `/tmp/fork-update-logs/`.

Robustness rules built into the script:

- A failing test is retried once, then run on bare upstream at the target. If it fails there too, it is an upstream or environment failure: the script reports it as `note: also fails on upstream, ignored` and does not block. Treat it as a known problem, not as a patch to fix.
- Below 8 GiB of RAM, the script caps Go and golangci-lint parallelism and sets `GOMEMLIMIT`, so small orbs are not OOM-killed.
- A shallow clone hides `fork/main`'s parents from jj. When the clone is shallow, `fork-update` first runs `fork-bootstrap`, which unshallows it and rebuilds jj's view.

### Ship button

The Amp project uses Custom Ship with `.agents/ship.md`: pressing Ship in an orb thread turns its changes into a `patch/*` series and runs `fork-update assemble --push`. Amp stores a copy of that prompt in the project, so after editing the file, run `amp projects update ajac-zero/ai-gateway --ship-behavior custom --custom-ship-prompt-file .agents/ship.md`.

### Agent fixes for one patch

When `check` reports a patch as `conflict` or `broken`, an agent updates that patch alone:

1. Bootstrap if needed, then fetch. Use the upstream commit you were given as the target, not whatever `main@upstream` is now.
2. Follow "Bring Every Patch Up to Date" below for this one patch, with that commit in place of `trunk()`. Keep the patch's commit series, resolve at the earliest conflicted commit, and fix API drift in the commit that introduced the affected code.
3. Do not add code from other patches, and do not touch `fork/main`, `main`, or any PR-head bookmark.
4. Verify that every commit in the series is conflict-free. In a throwaway worktree at the new tip, `go build ./...` must succeed, and `go vet` and `go test` must pass for the packages the patch touches. If the patch touches `api/`, regenerated files must produce no diff.
5. Move `patch/<name>` to the new tip and push only that bookmark: `jj git push --remote origin -b patch/<name>`.

### Scheduled fork owner

A scheduled Amp thread runs in an orb on project `ajac-zero/ai-gateway`, whose base branch is `fork/main`. It runs `fork-update apply --push`. When `check` reports a conflicted or broken patch, it starts one orb thread per such patch with the "Agent fixes for one patch" procedure, pinned to the same upstream commit. After every fixer reports back, it runs `fork-update assemble --push`. It resolves any conflicts between patches in the new merge, as described in "Resolve Integration Conflicts", before moving `fork/main`.

## Synchronize With Upstream

For a quick refresh of `fork/main` without touching the patches:

```bash
jj fork-sync
```

This fetches upstream and recalculates the fork parent set. Upstream changes that conflict with a stale patch then have to be resolved inside `fork/main`, so prefer rebasing the patches when the user asks to bring the fork up to date.

### Bring Every Patch Up to Date

Move each patch onto the newest upstream so that conflicts with upstream are resolved inside the patch that owns the code:

1. `jj git fetch --remote upstream`, then fast-forward `main` with `jj bookmark set main -r main@upstream`.
2. Before rewriting, save the current `fork/main` commit as a reference tree. The rebased patches together should reproduce it.
3. Copy each series with `jj duplicate '::patch/<name> ~ ::main@upstream' -o 'trunk()'`. Do not use `jj rebase -s`; see Rebase Safety below.
4. Resolve conflicts at the earliest conflicted commit of each series with `jj new <commit>`, editing, and `jj squash`. jj carries the resolution to descendants. Fix upstream API drift, such as renamed types or helpers, in the commit that introduced the affected code, so that every commit builds. Regenerate conflicted generated files instead of merging them by hand: run `make apigen apidoc`, then `go tool -modfile=tools/go.mod license-eye header fix`, because `apigen` strips license headers.
5. Move each `patch/*` bookmark to its new tip. Do not run `jj fork-refresh` on the old merge, because it would carry over resolutions that now live in the patches. Build a fresh merge with `jj new 'fork_parents()' -m "internal: assemble fork patch set"` and run `jj bookmark set fork/main -r @ --allow-backwards`. Compare the result with the reference tree; every difference should be intentional.
6. Rebasing rewrites published patch heads. Do not move a separate PR-head bookmark unless the user asks to update that PR.

### Rebase Safety

Avoid `jj rebase -s <old-change> -o trunk()` in this repository unless you have first inspected all descendants. `-s` rebases the selected change and every visible descendant, which can include obsolete pre-jj integration history.

Inspect first:

```bash
jj log -r '<change>::'
```

For a clean copy that leaves old descendants untouched, prefer:

```bash
jj duplicate <revision-set> -o trunk()
```

Then move the relevant bookmark to the duplicated tip after validating it. For badly tangled merge history, create a clean patch from the final net result rather than preserving meaningless integration merges.

## Refresh the Fork Branch

After adding, advancing, removing, or rewriting any `patch/*` bookmark:

```bash
jj fork-refresh
jj fork-conflicts
```

Run `jj fork-refresh` a second time after resolution. It should report that nothing changed.

The generated fork branch may omit `trunk()` as a direct parent when every selected patch already descends from the same trunk. That is expected.

## Resolve Integration Conflicts

List conflicts:

```bash
jj fork-conflicts
jj resolve --list -r 'fork_head()'
```

Edit the fork merge:

```bash
jj edit fork/main
```

Resolve files manually or with `jj resolve`. Keep the resolution in `fork/main` when it combines independent patch behavior. Do not contaminate one patch with another patch's code merely to make the fork merge cleanly.

Verify:

```bash
jj resolve --list -r 'fork_head()'
jj fork-refresh
```

No conflict output and an idempotent refresh are required.

In a colocated repository, `git status` can display `UU` for a file resolved inside a multi-parent jj working-copy merge even when `jj resolve --list` reports no conflicts. Treat jj as authoritative and validate the committed tree with builds/tests.

## Publish a Patch or PR

First inspect the push without changing the remote:

```bash
jj git push --remote origin --dry-run --bookmark <bookmark>
```

Check that only intended bookmarks move. Then push only when the user explicitly asks:

```bash
jj git push --remote origin --bookmark <bookmark>
```

Rewritten PR heads will be shown as sideways moves. Confirm the PR bookmark still points to the same commit as its `patch/*` selector before pushing.

Never push every bookmark implicitly. Name each intended bookmark.

## Publish the Fork Branch

Verify first:

```bash
jj fork-refresh
jj fork-conflicts
jj fork-stat
```

Run relevant tests, then dry-run:

```bash
jj git push --remote origin --dry-run --bookmark fork/main
```

Push only on explicit request:

```bash
jj git push --remote origin --bookmark fork/main
```

Build internal images from `fork/main`.

## Retire an Upstreamed Patch

After confirming the change exists in `main@upstream`:

```bash
jj git fetch --remote upstream
jj bookmark delete patch/<short-name>
jj fork-refresh
jj fork-conflicts
```

If a separate PR-head bookmark is no longer needed, delete it separately. Review remote deletion with:

```bash
jj git push --remote origin --deleted --dry-run
```

Only apply remote deletion after confirming the plan contains no active PR heads.

## Remove a Patch From the Fork Branch Only

Delete or rename only the `patch/*` selector. Keep the PR-head bookmark if upstream review remains active:

```bash
jj bookmark delete patch/<short-name>
jj fork-refresh
```

The commit remains retained by the PR bookmark.

## Validation Checklist

Before considering patch graph work complete:

```bash
jj status
jj log -r 'trunk() | fork_head() | fork_patches()'
jj fork-conflicts
jj fork-refresh
```

For every rewritten patch:

```bash
git merge-base upstream/main patch/<name>
git rev-list --left-right --count upstream/main...patch/<name>
git diff --stat upstream/main...patch/<name>
```

Also verify no accidental cross-patch ancestry was introduced and run tests covering all modified packages. Use `jj op log` and `jj undo` to recover from incorrect graph operations.
