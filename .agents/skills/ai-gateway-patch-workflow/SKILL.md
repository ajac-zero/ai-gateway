---
name: ai-gateway-patch-workflow
description: Use when changing, adding, rebasing, publishing, retiring, or deploying fork patches in this Envoy AI Gateway repository. Covers jj patch/*, tooling/*, and glue/* bookmarks, upstream PR heads, fork/main, integration conflicts, and upstream synchronization.
---

# AI Gateway Patch Workflow

Use `jj`, not Git, for version-control mutations in this colocated repository. Git commands are acceptable for read-only inspection.

## Repository Model

- `trunk()` is exactly `main@upstream`.
- Bookmarks come in three tiers. All three are selected for the fork branch by name:

  | Tier | Rooted at | Holds | Goes upstream |
  |---|---|---|---|
  | `patch/<name>` | `trunk()` | One independently upstreamable change | Yes |
  | `tooling/<name>` | `trunk()` | Fork-only tooling, such as this skill, `.agents/setup`, the Ship prompt, and the root `AGENTS.md` | Never |
  | `glue/<a>+<b>` | The tips of series `a` and `b` | Only the resolution of the conflicts between `a` and `b` | Never |

- "Series" means a `patch/*` or `tooling/*` bookmark. `fork_patches()` selects both, and `fork_glue()` selects `glue/*`.
- `fork/main` is the fork's combined branch: a generated multi-parent merge of `fork_parents()`, which is `heads(trunk() | fork_patches() | fork_glue())`. A glue descends from the series it names, so the merge takes the glue in their place. `fork/main` never contains hand-made resolutions; rebuilding it from scratch is always safe.
- A GitHub PR may require a separate legacy-named bookmark pointing to the same commit as its `patch/*` bookmark.
- Conflicts between otherwise independent series, including semantic ones such as duplicate declarations that only break the build, belong in a glue, never in either series or in `fork/main`. See "Resolve Integration Conflicts".
- Never develop directly in `fork/main`.

The repository-scoped jj config is versioned as `jj-repo-config.toml` next to this skill. In a new clone, run `scripts/fork-bootstrap` once; it colocates jj, adds the `upstream` remote, fetches, tracks the fork's bookmarks, and installs the config. Orbs run it from `.agents/setup`. The config provides:

```text
trunk()
fork_patches()
fork_glue()
fork_parents()
fork_head()

jj patch-new
jj fork-assemble
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
jj log -r 'trunk() | fork_head() | fork_patches() | fork_glue()'
```

`jj fork-assemble` restacks stale glues and rebuilds `fork/main` locally, without fetching, testing, or pushing. Use it instead of `jj fork-refresh` after changing any series or glue: `jj fork-refresh` reuses the old merge and does not move glues. It leaves `@` on a new empty commit on `fork/main`.

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
jj fork-assemble
```

`jj fork-assemble` restacks any glue that names the patch. If a glue now conflicts, resolve it as "Resolve Integration Conflicts" describes.

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
jj fork-assemble
```

If `jj fork-assemble` reports a conflicting pair, add a glue for it as "Resolve Integration Conflicts" describes. Series names must be unique across `patch/` and `tooling/` and must not contain `+`.

Only create a separate PR-head bookmark when publishing upstream requires a different branch name:

```bash
jj bookmark create <github-pr-head> -r patch/<short-name>
```

## Change Fork Tooling

Fork-only tooling, such as `.agents/` and this skill, lives in `tooling/*` series instead of `patch/*`. Tooling is rooted at `trunk()` and handled like a patch: `fork-update` rebases and checks it, and fixer agents fix it. It is never proposed upstream, and the retire steps below do not apply to it. Fix `tooling/agent-workflow` the same way as a patch:

```bash
jj new tooling/agent-workflow
# edit and test
jj bookmark set tooling/agent-workflow -r @
jj fork-assemble
```

A working copy on a series rooted at `trunk()` has no `.agents/` directory. `jj fork-assemble` then runs the `fork-update` from `tooling/agent-workflow`.

## Update an Existing Change In Place

Prefer a follow-up commit with `jj new <patch>` for normal fixes. If explicitly asked to amend an existing unpublished change:

```bash
jj edit <change-id>
```

After editing, jj automatically rebases descendants. Still run:

```bash
jj fork-assemble
```

Do not rewrite a published PR change without expecting a non-fast-forward bookmark update.

## Automated Updates

`scripts/fork-update` (run from the repository root) handles the routine path:

```bash
.agents/skills/ai-gateway-patch-workflow/scripts/fork-update check     # read-only report
.agents/skills/ai-gateway-patch-workflow/scripts/fork-update apply --push
.agents/skills/ai-gateway-patch-workflow/scripts/fork-update assemble --push
```

`check` replays each stale patch onto `main@upstream` in a throwaway worktree, then builds, vets, and tests the packages it touches. It reports each patch as `up-to-date`, `clean`, `conflict`, or `broken`. It exits 0 when nothing is stale, 10 when every stale patch is clean, and 20 when an agent is needed. `apply` mutates nothing unless every stale patch is clean. It then rebases them with jj, verifies that each jj result has the same tree as the checked replay, fast-forwards `main`, and assembles `fork/main`. `assemble` first restacks every glue whose parents are not the current tips of the series it names, using `jj duplicate` to carry the resolution, and reports a `glue conflict` if a restacked glue conflicts. It then builds a fresh merge of `fork_parents()`. If that merge conflicts, it names each conflicting pair so the pair can get a glue, and leaves `fork/main` alone. It moves `fork/main` only after a conflict-free merge passes build, vet, lint, generated-file, and unit-test checks. `check` and `apply` treat `tooling/*` like `patch/*`. `--push` pushes `main`, every `patch/*`, `tooling/*`, and `glue/*`, and `fork/main` by name. Check logs are written under `/tmp/fork-update-logs/`.

Robustness rules built into the script:

- A failing test is retried once, then run on bare upstream at the target. If it fails there too, it is an upstream or environment failure: the script reports it as `note: also fails on upstream, ignored` and does not block. Treat it as a known problem, not as a patch to fix.
- Below 8 GiB of RAM, the script caps Go and golangci-lint parallelism and sets `GOMEMLIMIT`, so small orbs are not OOM-killed.
- When `fork/main` already merges every series but differs from `fork/main@origin`, as after `jj fork-assemble`, `assemble` still runs the full checks on it before pushing.
- `assemble` refuses while one series contains another. Series are independent, so a nested one is a stale or renamed bookmark that `--push` would otherwise re-create on origin. Delete it, or rebuild it on `trunk()`.
- Orb snapshots can carry an older jj repo config. `fork-update` installs the `jj-repo-config.toml` next to it whenever the repo's copy differs.
- A shallow clone hides `fork/main`'s parents from jj. When the clone is shallow, `fork-update` first runs `fork-bootstrap`, which unshallows it and rebuilds jj's view.
- Several threads may ship at once. The repo config sets `remotes.origin.auto-track-bookmarks`, and `fork-update` also tracks every `patch/*`, `tooling/*`, and `glue/*` on origin after fetching. A bookmark that another thread pushed therefore becomes a local bookmark that `fork_parents()` includes. Before pushing, `--push` fetches origin again. It refuses with exit 20 if `fork/main` or any `patch/*`, `tooling/*`, or `glue/*` bookmark changed on origin since the run started. Rerun `fork-update assemble --push` to build a merge that includes the new work.

### Ship button

The Amp project uses Custom Ship with `.agents/ship.md`: pressing Ship in an orb thread turns its changes into a `patch/*` or `tooling/*` series, adds a glue if the series conflicts with another one, and runs `fork-update assemble --push`. Amp stores a copy of that prompt in the project, so after editing the file, run `amp projects update ajac-zero/ai-gateway --ship-behavior custom --custom-ship-prompt-file .agents/ship.md`.

### Agent fixes for one patch

When `check` reports a patch or tooling series as `conflict` or `broken`, an agent updates that series alone:

1. Bootstrap if needed, then fetch. Use the upstream commit you were given as the target, not whatever `main@upstream` is now.
2. Follow "Bring Every Patch Up to Date" below for this one patch, with that commit in place of `trunk()`. Keep the patch's commit series, resolve at the earliest conflicted commit, and fix API drift in the commit that introduced the affected code.
3. Do not add code from other patches, and do not touch `fork/main`, `main`, any `glue/*`, or any PR-head bookmark.
4. Verify that every commit in the series is conflict-free. In a throwaway worktree at the new tip, `go build ./...` must succeed, and `go vet` and `go test` must pass for the packages the patch touches. If the patch touches `api/`, regenerated files must produce no diff.
5. Move the series bookmark (`patch/<name>` or `tooling/<name>`) to the new tip and push only that bookmark, for example `jj git push --remote origin -b patch/<name>`.

### Scheduled fork owner

A scheduled Amp thread runs in an orb on project `ajac-zero/ai-gateway`, whose base branch is `fork/main`. It runs `fork-update apply --push`. When `check` reports a conflicted or broken patch, it starts one orb thread per such patch with the "Agent fixes for one patch" procedure, pinned to the same upstream commit. Each fixer runs in the agent mode given by the report's `[tier=low|medium|high]`, and a failed fixer is retried once at the next tier up. The scheduled run itself is a `low`-mode thread that only routes work. The tier thresholds are documented at `conflict_tier` and `broken_tier` in `scripts/fork-update`. After every fixer reports back, it runs `fork-update assemble --push`. If that reports a `glue conflict` or a conflicting pair, the owner resolves it in a glue itself, as described in "Resolve Integration Conflicts", and reruns `fork-update assemble --push`. It does not hand glue work to a separate thread: after `apply`, the rebased patches exist only in the owner's orb until the final push.

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
5. Move each series bookmark to its new tip. Run `jj fork-assemble`: it restacks every glue onto the new tips and builds a fresh `fork/main` merge. Do not run `jj fork-refresh` on the old merge. Resolve any reported glue conflict or conflicting pair as "Resolve Integration Conflicts" describes. Compare the result with the reference tree; every difference should be intentional.
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

After adding, advancing, removing, or rewriting any `patch/*`, `tooling/*`, or `glue/*` bookmark:

```bash
jj fork-assemble
```

It reports `fork/main already merges trunk() and every patch` when nothing changed. `jj fork-refresh` remains for a fork without glues, but `jj fork-assemble` is always correct.

The generated fork branch may omit `trunk()` as a direct parent when every selected patch already descends from the same trunk. That is expected.

## Resolve Integration Conflicts

Two series that each pass on their own can conflict when merged. The conflict may be textual, or semantic, such as two declarations of the same name that only break the build. Resolve it in a glue for that pair, never in either series and never in `fork/main`.

`jj fork-assemble` and `fork-update assemble` name each conflicting pair, for example `conflicting pair: patch/responses-anthropic + patch/responses-gcpvertexai`. For a semantic conflict, find the pair from the build error. To add the glue, merge the two series tips and resolve in that merge:

```bash
jj new 'bookmarks(exact:"patch/a")' 'bookmarks(exact:"patch/b")' -m "glue: a + b"
jj resolve --list
# edit until every conflict is resolved and the tree builds; keep both sides' behavior
jj bookmark create 'glue/a+b' -r @
jj fork-assemble
```

Glue rules:

- Name it `glue/<a>+<b>` with the series names, without the `patch/` or `tooling/` prefix. Quote names containing `+` in revsets, as in `'bookmarks(exact:"glue/a+b")'`.
- Its parents are exactly the tips of the named series. `fork-update` checks this and restacks a stale glue onto the current tips with `jj duplicate`, which carries the resolution.
- It contains only what combining the pair requires. Do not add features to a glue.
- When three series conflict in the same place, name all three, as in `glue/a+b+c`. Its parents are the heads of `patch/a`, `patch/b`, `patch/c`, and any glue over a subset of them. With `glue/a+b` present, that is `glue/a+b` and `patch/c`. `fork-update` derives this from the names.
- When a glue conflicts after a restack (`glue conflict` in the report), resolve inside the glue with `jj new <glue-commit>`, editing, and `jj squash`, then run `jj fork-assemble` again.

When a merge conflicts but no single pair does, `fork-update` falls back to suggesting `--candidate`: resolve inside the reported merge and run `fork-update assemble --candidate <merge> --push`. This should be rare; prefer a three-way glue.

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

Rewritten PR heads will be shown as sideways moves. Confirm the PR bookmark still points to the same commit as its `patch/*` selector before pushing. Push a new or restacked glue the same way, naming its `glue/*` bookmark.

Never push every bookmark implicitly. Name each intended bookmark.

## Publish the Fork Branch

Verify first:

```bash
jj fork-assemble
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
jj bookmark list 'glue/*'
jj fork-assemble
```

Also delete every `glue/*` whose name includes the retired patch. A glue descends from the series it merges, so a leftover glue would bring the retired patch back into `fork/main`. `fork-update` refuses to assemble while a glue names a missing series. If the remaining series in a deleted glue still conflict with each other, add a glue for them.

This section applies only to `patch/*`; `tooling/*` series are never upstreamed.

If a separate PR-head bookmark is no longer needed, delete it separately. Review remote deletion with:

```bash
jj git push --remote origin --deleted --dry-run
```

Only apply remote deletion after confirming the plan contains no active PR heads.

## Remove a Patch From the Fork Branch Only

Delete or rename only the `patch/*` selector. Keep the PR-head bookmark if upstream review remains active:

```bash
jj bookmark delete patch/<short-name>
jj fork-assemble
```

The commit remains retained by the PR bookmark. As when retiring, delete every `glue/*` that names the patch.

## Validation Checklist

Before considering patch graph work complete:

```bash
jj status
jj log -r 'trunk() | fork_head() | fork_patches() | fork_glue()'
jj fork-assemble
```

`jj fork-assemble` must report no glue conflict, no conflicting pair, and, when run a second time, that `fork/main` already merges every patch.

For every rewritten series:

```bash
git merge-base upstream/main patch/<name>
git rev-list --left-right --count upstream/main...patch/<name>
git diff --stat upstream/main...patch/<name>
```

Also verify no accidental cross-patch ancestry was introduced: only glues may descend from more than one series. Run tests covering all modified packages. Use `jj op log` and `jj undo` to recover from incorrect graph operations.
