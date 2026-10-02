# Fork conventions

This checkout is a fork of [envoyproxy/ai-gateway](https://github.com/envoyproxy/ai-gateway). The `fork/main` branch is generated: it merges upstream `main` with every `patch/*` and `tooling/*` series and every `glue/*` integration merge. Every change ends up in one of those series.

- Load the `ai-gateway-patch-workflow` skill before any version-control work: committing, creating or moving bookmarks, rebasing, pushing, or shipping. Use `jj`, not Git, to change history. Never commit onto `fork/main` or `main`.
- Plain feature work needs no setup. Edit the working copy and verify as usual. The Ship button turns the result into a series following the skill.
- A new feature becomes its own `patch/<name>`, rooted on upstream `main`. It must build and pass its tests on upstream alone, so do not depend on code that only another patch adds. The working copy includes every patch, so check before reusing a helper, type, or file: `git grep -n <name> upstream/main` shows whether it exists upstream, and `jj log -r 'fork_patches()'` lists the patches. If the feature extends an existing patch, say so; it then belongs in that patch's series.
- Changes to fork-only tooling (`.agents/`, `AGENTS.md`, the skill) belong in `tooling/agent-workflow`, not in a patch.
