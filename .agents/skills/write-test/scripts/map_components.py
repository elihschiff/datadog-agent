#!/usr/bin/env python3
# Unless explicitly stated otherwise all files in this repository are licensed
# under the Apache License Version 2.0.
# This product includes software developed at Datadog (https://www.datadoghq.com/).
# Copyright 2026-present Datadog, Inc.
"""Map Go source paths to the Agent components they belong to, and list how to wire each component in a test.

Usage, from anywhere in the repository:
    map_components.py                # the Go files changed against the merge base with origin/main
    map_components.py <path>...      # Go files or directories, e.g. the ones a feature touches
    map_components.py --all ...      # list every consumer instead of the first ones

For every component or bundle reached, it prints its team, the fx modules and mocks available to wire it in a test,
and the packages that import its interface or its fx modules (its consumers and the packages wiring it, the next
hops of the pipeline). A pkg/ path is mapped to the
components importing it. The output is Markdown, meant to be read by the write-test skill.
"""

import functools
import os
import re
import subprocess
import sys
from collections import defaultdict

MODULE = "github.com/DataDog/datadog-agent"
# subdirectories that hold a way to build the component in a test, from the most to the least real
WIRING_DIRS = ["fx", "impl", "fx-mock", "mock", "fx-noop", "noop-impl"]
# subdirectories of a component that are not components themselves
COMPONENT_SUBDIRS = set(WIRING_DIRS) | {"def"}
MAX_CONSUMERS = 8


def git(*args):
    return subprocess.run(["git", *args], check=True, capture_output=True, text=True).stdout


def changed_go_files():
    """Return the merge base and the Go files changed since it, deleted ones included: they are entry points too."""
    try:
        base = git("merge-base", "HEAD", "origin/main").strip()
    except subprocess.CalledProcessError:
        sys.exit("origin/main not found: fetch it (git fetch origin main), or pass the paths to map explicitly.")
    tracked = git("diff", "--name-only", base).splitlines()
    untracked = git("ls-files", "--others", "--exclude-standard").splitlines()
    return base, sorted({p for p in tracked + untracked if p.endswith(".go")})


def expand(paths):
    """Return the Go files of the given files and directories, warning about the arguments that hold none."""
    files = []
    for path in paths:
        if os.path.isdir(path):
            listed = git("ls-files", "--cached", "--others", "--exclude-standard", "--", path).splitlines()
            found = [p for p in listed if p.endswith(".go") and os.path.exists(p)]
        elif os.path.isfile(path) and path.endswith(".go"):
            found = [path]
        else:
            found = []
        if not found:
            print(f"warning: ignoring `{path}`: not a Go file or a directory holding Go files", file=sys.stderr)
        files += found
    return files


def group_by_package(files):
    packages = defaultdict(list)
    for path in files:
        packages[os.path.dirname(path)].append(path)
    return packages


@functools.cache
def package_root(directory):
    """Return (root, kind) of the component or bundle containing the package directory, or (None, None)."""
    while directory.startswith("comp/"):
        # def/, fx/, impl/... belong to the component above them, even when they hold a component.go
        if os.path.basename(directory) not in COMPONENT_SUBDIRS:
            if os.path.exists(os.path.join(directory, "def", "component.go")):
                return directory, "component"
            if os.path.exists(os.path.join(directory, "component.go")):
                return directory, "component (legacy layout)"
            if os.path.exists(os.path.join(directory, "bundle.go")):
                return directory, "bundle"
        directory = os.path.dirname(directory)
    return None, None


def team(root):
    for name in ("def/component.go", "component.go", "bundle.go"):
        candidate = os.path.join(root, name)
        if os.path.exists(candidate):
            with open(candidate, encoding="utf-8") as f:
                match = re.search(r"^// team: (.+)$", f.read(), re.MULTILINE)
            if match:
                return match.group(1).strip()
    return "?"


def declares_module(directory):
    """Return whether a non-test Go file of the directory declares a Module() function."""
    for name in os.listdir(directory):
        if name.endswith(".go") and not name.endswith("_test.go"):
            with open(os.path.join(directory, name), encoding="utf-8") as f:
                if re.search(r"^func Module\(", f.read(), re.MULTILINE):
                    return True
    return False


def wiring(root, kind):
    """List what can build the component in a test."""
    found = [f"`{d}/`" for d in WIRING_DIRS if os.path.isdir(os.path.join(root, d))]
    if kind == "bundle":
        found.insert(0, "`Bundle()`")
    elif kind == "component (legacy layout)":
        # the root package holds the fx module and, often, the mock
        if declares_module(root):
            found.insert(0, "root `Module()`")
        found += [f"`{name}`" for name in sorted(os.listdir(root)) if name.endswith("_mock.go")]
    return found


def public_packages(root):
    """Return the packages other code imports to use or wire the component: its interface and its fx modules.

    Importers of def/ use the component; importers of fx/ (or fx-mock/, fx-noop/) assemble it, which is how a bundle
    or a binary that wires the pipeline shows up.
    """
    packages = [os.path.join(root, d) for d in ["def"] + [w for w in WIRING_DIRS if w.startswith("fx")]]
    packages = [p for p in packages if os.path.isdir(p)]
    return packages or [root]


@functools.cache
def importers(package_dir):
    """Return the directories of the non-test Go files importing package_dir."""
    pattern = f'"{MODULE}/{package_dir}"'
    try:
        # --untracked: a new component's importers may not be committed yet, like the component itself
        output = git("grep", "-l", "--untracked", "--fixed-strings", pattern, "--", "*.go", ":!*_test.go")
    except subprocess.CalledProcessError:  # no match
        return frozenset()
    return frozenset(os.path.dirname(p) for p in output.splitlines())


def main():
    # paths are matched against repository-relative prefixes such as comp/: resolve the arguments from where they were
    # typed, then work from the repository root
    args = sys.argv[1:]
    show_all = "--all" in args
    args = [a for a in args if a != "--all"]
    root = git("rev-parse", "--show-toplevel").strip()
    paths = [os.path.relpath(os.path.abspath(p), root) for p in args]
    os.chdir(root)
    base = "origin/main"
    if paths:
        files = expand(paths)
    else:
        base, files = changed_go_files()
        base = base[:12]
    if not files:
        print("No Go file to map.")
        return

    changed = defaultdict(set)  # root -> changed files
    kinds = {}
    via_pkg = defaultdict(set)  # root -> pkg/ packages it imports
    unmapped = {}  # package -> number of files
    for package_dir, package_files in group_by_package(files).items():
        root, kind = package_root(package_dir)
        if root:
            changed[root].update(package_files)
            kinds[root] = kind
            continue
        reached = {package_root(d) for d in importers(package_dir)} - {(None, None)}
        if not reached:
            unmapped[package_dir] = len(package_files)
        for root, kind in reached:
            via_pkg[root].add(package_dir)
            kinds[root] = kind

    print("| Component | Kind | Team | Reached by | Test wiring | Consumers |")
    print("| --- | --- | --- | --- | --- | --- |")
    for root in sorted(set(changed) | set(via_pkg)):
        reached_by = sorted(
            f"`{os.path.relpath(p, root)}`" + ("" if os.path.exists(p) else " (deleted)") for p in changed.get(root, ())
        )
        reached_by += sorted(f"imports `{p}`" for p in via_pkg.get(root, ()))
        consumers = sorted(
            {package_root(d)[0] or d for package in public_packages(root) for d in importers(package)} - {root}
        )
        listed = consumers if show_all else consumers[:MAX_CONSUMERS]
        shown = ", ".join(f"`{c}`" for c in listed)
        if len(consumers) > len(listed):
            shown += f", +{len(consumers) - len(listed)} more (--all lists them)"
        print(
            f"| `{root}` | {kinds[root]} | {team(root)} | {', '.join(reached_by)} "
            f"| {', '.join(wiring(root, kinds[root])) or '-'} | {shown or '-'} |"
        )
    if unmapped:
        print(
            "\nNo component imports these packages directly: pass their importers (`git grep -l '\"<import path>\"'`)"
        )
        print("to this script to follow the pipeline up to a component, or test them as plain packages:")
        for package_dir in sorted(unmapped):
            print(f"- `{package_dir}` ({unmapped[package_dir]} file(s))")
    deleted = sorted(p for p in unmapped if not os.path.isdir(p))
    if deleted:
        # their importers changed or are gone too, so they can only be followed at the merge base
        print(f"\nDeleted entirely, so follow them at the merge base `{base}`:", ", ".join(f"`{p}`" for p in deleted))
        print(f"`git show {base}:<path>` reads a file, `git grep -l '\"<import path>\"' {base}` its importers.")


if __name__ == "__main__":
    main()
