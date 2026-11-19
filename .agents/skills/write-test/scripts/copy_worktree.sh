#!/usr/bin/env bash
# Unless explicitly stated otherwise all files in this repository are licensed
# under the Apache License Version 2.0.
# This product includes software developed at Datadog (https://www.datadoghq.com/).
# Copyright 2026-present Datadog, Inc.
#
# Copy the working tree into a throwaway detached worktree under .worktrees/ and print its absolute path: HEAD, plus the
# staged and unstaged changes to tracked files (modes and binary files included), plus the untracked files that aren't
# ignored. Nothing is written to the repository's object store, the index or the original files. Exits non-zero,
# printing nothing on stdout and removing the partial copy, when any step fails.
set -euo pipefail

top=$(git rev-parse --show-toplevel)
cd "$top"
dir=".worktrees/write-test-$(date +%Y%m%d%H%M%S)-$$"
patch=$(mktemp)
cleanup() {
    rm -f "$patch"
}
trap cleanup EXIT

git worktree add --quiet --detach "$dir" HEAD
trap 'git worktree remove --force "$dir" >/dev/null 2>&1 || true; cleanup' ERR

# explicit prefixes and no external diff or color: diff.noprefix, diff.external or color.diff=always in the
# contributor's configuration would otherwise produce a patch git apply rejects
git diff --binary --no-color --no-ext-diff --src-prefix=a/ --dst-prefix=b/ HEAD > "$patch"
if [[ -s $patch ]]; then
    git -C "$dir" apply "$patch"
fi
git ls-files -z --others --exclude-standard | tar --null -T - -cf - | tar -C "$dir" -xf -
echo "$top/$dir"
