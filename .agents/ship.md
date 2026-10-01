Ship this thread's changes as a fork patch by following the ai-gateway-patch-workflow skill. This repository is a fork: `fork/main` is generated from upstream `main` plus every `patch/*` branch, so never commit onto `fork/main` or `main`.

1. Decide where the changes belong. If they fix or extend one existing `patch/<name>`, add commits on top of that patch with `jj new patch/<name>`. If they are a new independent change, start a new series with `jj patch-new` and name it `patch/<short-name>`. If it is unclear which patch the changes belong to, or they span several patches, stop and ask me.
2. Move the changes onto that patch so its series contains only this work, rooted on upstream `main`. Do not include code from other patches.
3. Verify the patch on its own, as "Agent fixes for one patch" in the skill describes: no conflicts, `go build ./...`, and `go vet` and `go test` for the packages it touches. If it touches `api/`, regenerate the generated files.
4. Move the `patch/<name>` bookmark to the new tip and push only that bookmark: `jj git push --remote origin -b patch/<name>`.
5. Run `.agents/skills/ai-gateway-patch-workflow/scripts/fork-update assemble --push` to rebuild, check, and publish `fork/main`. If it reports a conflict between patches, resolve it in the reported merge as the skill describes and run `fork-update assemble --candidate <merge> --push`.
6. Report the patch name and its new tip, the new `fork/main` commit, and any test that `fork-update` noted as failing on upstream too. Then archive this thread.
