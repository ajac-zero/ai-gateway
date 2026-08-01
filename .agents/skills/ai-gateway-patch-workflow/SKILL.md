---
name: ai-gateway-patch-workflow
description: Use when changing, adding, rebasing, publishing, retiring, or deploying fork patches in this Envoy AI Gateway repository. Covers jj patch/* bookmarks, upstream PR heads, deploy/integration, deploy-refresh, conflict resolution, and upstream synchronization.
---

# AI Gateway Patch Workflow

Use `jj`, not Git, for version-control mutations in this colocated repository. Git commands are acceptable for read-only inspection.

## Repository Model

- `trunk()` is exactly `main@upstream`.
- Every independently upstreamable change is rooted at `trunk()` and selected for internal deployment by a local `patch/*` bookmark.
- `deploy/integration` is a generated multi-parent merge of `deploy_parents()`.
- A GitHub PR may require a separate legacy-named bookmark pointing to the same commit as its `patch/*` bookmark.
- Conflict resolutions between otherwise independent patches belong in `deploy/integration`, not in either patch.
- Never develop directly in `deploy/integration` except to resolve integration-only conflicts.

The repository-scoped jj config provides:

```text
trunk()
deploy_patches()
deploy_parents()
deploy_head()

jj patch-new
jj deploy-refresh
jj deploy-sync
jj deploy-log
jj deploy-parents
jj deploy-conflicts
jj deploy-stat
```

## Before Any Patch Work

Inspect the graph and working copy:

```bash
jj status
jj log -r 'trunk() | deploy_head() | deploy_patches()'
```

Do not modify unrelated changes. If `@` is `deploy/integration`, start patch work with `jj new <patch-bookmark>` or `jj new trunk()` rather than editing `@`.

## Fix an Existing Patch

For example, to fix `patch/native-gemini-ingress`:

```bash
jj new patch/native-gemini-ingress
jj describe -m "fix: describe the bug"
```

Edit and test the code. Then advance the selector:

```bash
jj bookmark set patch/native-gemini-ingress -r @
jj deploy-refresh
jj deploy-conflicts
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

Start from upstream, never from `deploy/integration`:

```bash
jj patch-new -m "fix: describe the change"
```

Edit and test, then create the deployment selector:

```bash
jj bookmark create patch/<short-name> -r @
jj deploy-refresh
jj deploy-conflicts
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
jj deploy-refresh
```

Do not rewrite a published PR change without expecting a non-fast-forward bookmark update.

## Synchronize With Upstream

For routine deployment refresh:

```bash
jj deploy-sync
```

This fetches upstream and recalculates the deployment parent set. It intentionally does not rewrite every patch whenever upstream advances.

If an individual patch must become directly based on the newest upstream for an upstream PR, rebuild or rebase that patch deliberately and verify its net diff. Do not bulk-rebase all patches just because upstream moved.

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

## Refresh Deployment

After adding, advancing, removing, or rewriting any `patch/*` bookmark:

```bash
jj deploy-refresh
jj deploy-conflicts
```

Run `jj deploy-refresh` a second time after resolution. It should report that nothing changed.

The generated deployment may omit `trunk()` as a direct parent when every selected patch already descends from the same trunk. That is expected.

## Resolve Integration Conflicts

List conflicts:

```bash
jj deploy-conflicts
jj resolve --list -r 'deploy_head()'
```

Edit the deployment merge:

```bash
jj edit deploy/integration
```

Resolve files manually or with `jj resolve`. Keep the resolution in `deploy/integration` when it combines independent patch behavior. Do not contaminate one patch with another patch's code merely to make deployment merge cleanly.

Verify:

```bash
jj resolve --list -r 'deploy_head()'
jj deploy-refresh
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

## Publish Deployment

Verify first:

```bash
jj deploy-refresh
jj deploy-conflicts
jj deploy-stat
```

Run relevant tests, then dry-run:

```bash
jj git push --remote origin --dry-run --bookmark deploy/integration
```

Push only on explicit request:

```bash
jj git push --remote origin --bookmark deploy/integration
```

Build internal images from `deploy/integration`.

## Retire an Upstreamed Patch

After confirming the change exists in `main@upstream`:

```bash
jj git fetch --remote upstream
jj bookmark delete patch/<short-name>
jj deploy-refresh
jj deploy-conflicts
```

If a separate PR-head bookmark is no longer needed, delete it separately. Review remote deletion with:

```bash
jj git push --remote origin --deleted --dry-run
```

Only apply remote deletion after confirming the plan contains no active PR heads.

## Remove a Patch From Internal Deployment Only

Delete or rename only the `patch/*` selector. Keep the PR-head bookmark if upstream review remains active:

```bash
jj bookmark delete patch/<short-name>
jj deploy-refresh
```

The commit remains retained by the PR bookmark.

## Validation Checklist

Before considering patch graph work complete:

```bash
jj status
jj log -r 'trunk() | deploy_head() | deploy_patches()'
jj deploy-conflicts
jj deploy-refresh
```

For every rewritten patch:

```bash
git merge-base upstream/main patch/<name>
git rev-list --left-right --count upstream/main...patch/<name>
git diff --stat upstream/main...patch/<name>
```

Also verify no accidental cross-patch ancestry was introduced and run tests covering all modified packages. Use `jj op log` and `jj undo` to recover from incorrect graph operations.
