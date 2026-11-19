#!/usr/bin/env bash
# Unless explicitly stated otherwise all files in this repository are licensed
# under the Apache License Version 2.0.
# This product includes software developed at Datadog (https://www.datadoghq.com/).
# Copyright 2026-present Datadog, Inc.
#
# Print the git tree ID of the whole working tree, with staged, unstaged and untracked (not ignored) files and their
# modes, computed on a copy of the index so the real one is untouched. Exits non-zero, printing nothing on stdout, when
# any git command fails.
set -euo pipefail

index=$(mktemp)
trap 'rm -f "$index"' EXIT
cp "$(git rev-parse --git-path index)" "$index"
GIT_INDEX_FILE=$index git add -A
tree=$(GIT_INDEX_FILE=$index git write-tree)
[[ $tree =~ ^[0-9a-f]{40,64}$ ]]
echo "$tree"
