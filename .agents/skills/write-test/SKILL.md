---
name: write-test
description: Write the Go test a Datadog Agent change needs, at the right level — a unit test, an integration test wiring the real components with fx and a local fakeintake, or a hand-off to new-e2e. Finds the components the current diff touches, or asks which feature to test and maps the components it involves, then writes, wires and validates the test. Use when asked to "write a test", "add tests for my change", "test this feature", "cover this end to end", "make this an integration test", or "replace this e2e test with something cheaper". Not for writing or extending new-e2e tests the user already asked for by name (write-e2e), running existing tests (run-e2e), judging an existing suite only (e2e-audit), or Python tests.
allowed-tools: Read, Write, Edit, Glob, Grep, Bash, Skill, AskUserQuestion
argument-hint: "[feature or behavior to test — omit to derive it from the current diff]"
---

Produce a test that catches the failures the change could introduce, at the cheapest level that still catches them. The usual mistake this skill prevents is jumping to the extremes: a unit test that mocks away the boundary that breaks, or a new-e2e test that provisions a VM for behavior an in-process integration test covers in seconds.

**Owning team:** `@DataDog/agent-devx`

**Prerequisites:** run from the repository root, with `dda` and Bazel set up (`dda inv install-tools`) and `origin/main` fetched. Go tests only: Python checks and invoke tasks are out of scope.

## References

| File | Load when |
|---|---|
| `references/integration.md` | Step 3 picks an integration test — before writing it. It holds the wiring patterns and the pitfalls that make such tests weak or flaky. |
| `doc/guidelines/testing/test-categories.md` | You need the repository's definition of a test category to explain a verdict. |

## Step 0 — Establish the scope

With an argument, treat it as the feature to test. Otherwise derive the scope from the change:

```bash
git status --short
python3 -I .agents/skills/write-test/scripts/map_components.py
```

The script maps every Go file changed against `origin/main`, untracked new files included, which `git diff` alone would miss. It ignores other files, so read the rest of `git status --short` and `git diff --name-only origin/main...` too: a changed schema (`pkg/config/schema/yaml/`), profile or embedded YAML file names a behavior on its own, the effect of the setting or entry it changes, and its loader is the entry point for Step 1. When neither the Go files nor these identify a behavior, or the user wants to test something other than the diff, ask which feature or behavior to test with `AskUserQuestion`, offering the candidates you can see (recent commits, the files they mention). A behavior is something a user, another component, or Datadog can observe: a payload sent to intake, a status or diagnose output, a config key's effect, a CLI result. Restate it in that form before going on, because the rest of the procedure maps and tests *that* behavior, not the files.

## Step 1 — Map the components involved

List the components the behavior flows through, from where its input enters to where its output is observed. These are the candidates to wire for real in a test.

1. **Find the entry points.**
   - From the diff: take the changed Go files.
   - From a feature: find the code that implements it. Search for what a user would name: the config key (use the `locate-config-setting` skill for its schema node), the metric, tag, issue, or log message, the CLI subcommand, the intake endpoint. `git grep -n` the literal and keep the non-test hits.
2. **Map them to components:**
   ```bash
   python3 -I .agents/skills/write-test/scripts/map_components.py [<path>...]
   ```
   Without arguments it maps the Go files changed against `origin/main`. For each component or bundle reached, it prints the team, the directories that can build it in a test (`fx/`, `impl/`, `fx-mock/`, `mock/`, `fx-noop/`, `noop-impl/`, or `Bundle()`), and its consumers. A `pkg/` package is mapped to the components importing it; when none imports it directly, pass its importers to the script to follow the chain. The consumers column lists the first 8 alphabetically; pass `--all` when you need them all, for example to find the bundle that wires a widely used component.
3. **Follow the pipeline.** The script maps Go code only: changed configuration such as a profile, a schema or an embedded YAML file (`defaultProfiles.yaml`, `pkg/config/schema/yaml/`) is a hop too, often the one that disagrees with the code, so add it to the chain. Read the `Requires` struct of each changed component's implementation (`impl/*.go`) for its upstream dependencies, and the consumers column for downstream ones. Stop at the boundary where the behavior becomes observable: a forwarder sending to intake, a status provider, a diagnose function, a file written. Read the code along the way rather than assuming the data path from names.
4. **Report the pipeline** before choosing a level, as a short chain with each hop's component and what it contributes, for example:

   > `datadog.yaml` + `DD_*` env → `comp/core/config` (loads, validates with `comp/core/schema`) → `comp/healthplatform` invalid-config check → store → forwarder → intake `/api/v2/agenthealth`

When the user named a feature and the mapping is ambiguous (several implementations, or a feature split across binaries), ask which path matters rather than testing all of them.

## Step 2 — Choose the level

Invoke the `e2e-audit` skill with the behavior and the pipeline from Step 1. It decides between a unit test, an integration test and new-e2e, by asking which boundary each assertion needs to keep real. Then:

The audit classifies each assertion, and the assertions of one behavior can land at different levels. Split them:

- **Assertions needing E2E:** invoke `write-e2e` with only those assertions, prefixed with `e2e-audit verdict: E2E justified — <reason>` and the hops the audit named as needing the deployed Agent. The prefix lets `write-e2e` reuse this session's audit instead of running it again, so the two skills can't send the behavior back and forth.
- **Integration assertions:** load `references/integration.md` and continue with them.
- **Unit assertions:** continue with them.
- **Uncertain:** ask for the missing assertion or boundary the audit named, and audit again.

Wait for `write-e2e` to finish and keep its result: whether it wrote the test, and which of its checks ran. When every assertion was handed off, write the final report (see Output) from that result, then stop; otherwise the lower-level assertions stay in scope here.

A behavior often needs both: unit tests for the logic of the changed component, and one integration test proving the pipeline still carries it end to end. That pipeline test is the one most changes are missing — unit tests on each piece don't catch two pieces disagreeing (a schema rejecting what the loader produces, a payload field the forwarder drops). Propose it whenever the pipeline spans more than one component, even if the user asked for a unit test.

## Step 3 — Write the test

Read the existing tests next to the code first and follow their conventions: helpers, mocks, table layout, and build tags. The repository's test conventions win over this skill.

**Unit test**, in the package of the logic under test (`impl/` for a component):

- Table-driven, one case per behavior, named after the behavior.
- Mock only what the logic doesn't own, with the component's own `mock/` or `fx-mock/` package, or `config.NewMockWithOverrides` for settings.
- Assert on outputs and errors, not on calls to mocks, unless the call is the behavior.

**Integration test:** follow `references/integration.md`. In short: build the pipeline with the real `fx/` modules of every hop from Step 1, feed the input the way production does, capture the output at the observable boundary, and assert on all of the output the behavior's contract covers: a golden file when the whole payload is the contract, every series of a metric when it is one part of a shared payload.

For both, match the header and build tags of the package's other test files, and keep each test independent: no order dependency, no global state left behind.

## Step 4 — Wire it into the build

```bash
bazel run //:gazelle -- <package dir>
```

Run it after adding a test file or `testdata/` files, and after changing the imports of an existing test, which changes its target's `deps`. Keep the generated `BUILD.bazel` diff with the test changes: CI fails without it (`bazel:run-gazelle`). Committing is up to the contributor. Check that files the test reads at runtime are in the test target's `data` (gazelle adds `glob(["testdata/**"])`) and that `go:embed` files are in `embedsrcs`. Gazelle doesn't always remove entries for deleted files; remove those by hand. When a `BUILD.bazel` file changed, run `bazel run //bazel/buildifier:test`, and on failure `bazel run //bazel/buildifier` then the test again.

## Step 5 — Validate

1. Run it: `dda inv test --targets=./<package> --test-args='-test.v'`. Read the subtest results in the Bazel test log, not only the target status. Its path is printed for a failing target; for a passing one, targets with build-tag flavors live under a transitioned configuration that `bazel-testlogs` doesn't point to, so find it with `find "$(bazel info output_path)" -path '*/testlogs/<package>/*/test.log'`: a test that skips (for example, when a generated file is missing) passes too, and only `-test.v` prints `--- PASS` and `--- SKIP` per subtest. Add `-test.run=TestX/<case>` to iterate on one case.
2. Prove it fails, by breaking the behavior once and checking the test catches it. The change under test may have uncommitted, staged and untracked edits (a new fixture), so the tree is snapshotted first and restored from that snapshot, never by undoing the break by hand.
   1. **Look for a leftover break.** `find "$(git rev-parse --absolute-git-dir)" -maxdepth 1 -name 'write-test-baseline.*'` lists the baselines of earlier runs in this worktree. One that this run didn't create means an earlier run stopped between its break and its restore, or another run is in progress: show it to the contributor and ask before going on, since its break may still be in the tree.
   2. **Snapshot the tree** into a baseline file of its own, so overlapping runs can't overwrite each other's:
      ```bash
      baseline=$(mktemp "$(git rev-parse --absolute-git-dir)/write-test-baseline.XXXXXX") && .agents/skills/write-test/scripts/snapshot_tree.sh > "$baseline" && [ -s "$baseline" ] && echo "$baseline" || { echo "no baseline snapshot: don't break anything" >&2; false; }
      ```
      It prints the baseline's path. Shell variables don't survive across commands, so use that path literally in the commands below. The script prints the staged tree and the tree of everything on disk that isn't ignored, computed on a copy of the index, and fails, printing nothing, on any git error. Break nothing until this command succeeds.
   3. **Choose the break and give its restore command.** The break is one edit to one file that already exists (an input fixture, a condition in the code), named by its path from the repository root. Don't create, delete or stage a file, which restoring can't undo. Check the file is unchanged since the snapshot:
      ```bash
      [ "$(git hash-object <file>)" = "$(git rev-parse "$(cut -d' ' -f2 <baseline>):<file>")" ]
      ```
      Then tell the contributor the command that restores it, so that an interruption leaves them a way back:
      ```bash
      git restore --source="$(cut -d' ' -f2 <baseline>)" --worktree -- <file>
      ```
      It writes the file back from the snapshot's working tree, and leaves the index alone.
   4. **Break and run.** Make the edit, then record the broken file's hash, `git hash-object <file>`. Run the test. It must fail with a message naming the problem: a test that still passes on the break was asserting on something else.
   5. **Restore, whatever the result,** but only the break: check that `git hash-object <file>` still prints the hash you recorded, then run the restore command. A different hash means the file was edited after the break, and restoring would erase that edit: don't restore, show the contributor the file's diff against the snapshot (`git diff "$(cut -d' ' -f2 <baseline>)" -- <file>`) and stop.
   6. **Check the tree is restored:**
      ```bash
      [ -s <baseline> ] && .agents/skills/write-test/scripts/snapshot_tree.sh | cmp -s - <baseline> && rm <baseline>
      ```
      Treat a failed snapshot as a failed check, never as a match. On success the baseline is removed. A mismatch means a file other than the break changed since the snapshot, possibly by the contributor: keep the baseline, show `git status --short`, restore nothing else, and stop.

   The break runs in place rather than in a copy of the worktree, since a copy starts Bazel from an empty output base and rebuilds everything. When the check can't be completed safely (no baseline, a leftover the contributor didn't clear, a file edited during the check, or a tree that doesn't match after the restore), don't break anything more. Report `Validated: deliberate break not run — <reason>` or `tree not restored — <what differs>, baseline kept at <path>`, and mark the test **incomplete**.
3. Rerun the test on the restored tree and check it passes again: the final result, not the first run, is the one to report.
4. Lint: `dda inv linter.go --targets=./<package>`. Typecheck other platforms when the test has platform-specific code (see the root `AGENTS.md`).

## Output

Report:

```markdown
**Behavior:** <the behavior, as observable>
**Pipeline:** <the chain from Step 1>
**Level:** <unit | integration | new-e2e> — <the e2e-audit reason, one line>

| Test | Package | Real | Mocked, and why |
| --- | --- | --- | --- |
| `TestX/<case>` | `comp/...` | <components wired for real> | <dependency>: <outside the boundary because…> |

**Validated:** <the deliberate break and the failure it produced> · <the snapshot matching after the restore> · <the rerun passing on the restored tree> · <lint result>
**Handed to write-e2e:** <the assertions, and its result: test written or not, and the checks it ran> (omit when nothing was handed off)
**Not covered:** <boundaries this test doesn't preserve, if any, and whether another level covers them>
```

With assertions at several levels, give the Level and Validated lines once per test. When every assertion was handed off, the report keeps Behavior, Pipeline, a Level line per assertion, Handed to write-e2e, and Not covered; drop the table and the Validated line, since no test was written here, and mark each assertion whose test `write-e2e` didn't write or validate as **incomplete**, with the reason.

The skill succeeded when every assertion has a test that was written and validated, here or by `write-e2e`; report an assertion whose test `write-e2e` didn't write or validate as incomplete, with the reason. For each test written here: it failed on the deliberate break, the tree snapshot matches after restoring it, the rerun passes, lint is clean, and every mock in the table has a reason that places it outside the behavior's boundary. An integration test asserts on everything the behavior's contract covers (see "Assert on what the contract covers" in the integration reference); for a unit test, every case of the table asserts on an output or an error.

## Example

**Input:** "Test that invalid settings in `datadog.yaml` reach intake as health issues, without leaking their values."

**Report:**

```markdown
**Behavior:** an invalid setting in `datadog.yaml`, `system-probe.yaml` or a `DD_*` variable reaches intake as an agent health issue naming the setting, without its value
**Pipeline:** `datadog.yaml`, `system-probe.yaml` and `DD_*` env → `comp/core/config` and `comp/core/sysprobeconfig` (load, validate with `comp/core/schema`) → `comp/healthplatform` checks → store → forwarder → intake `/api/v2/agenthealth`
**Level:** integration — every hop runs in-process, and intake is replaced by a local fakeintake without losing the payload boundary

| Test | Package | Real | Mocked, and why |
| --- | --- | --- | --- |
| `TestInvalidConfigPayload/<scenario>`, 9 scenarios | `comp/healthplatform` | config and sysprobeconfig loading from files and `DD_*` variables, schema validation, healthplatform checks and store, forwarder | intake: an in-process fakeintake, which receives the same HTTP payload · secrets: the secrets mock, resolving `ENC[]` handles from `secrets.json`, since settings are validated before their handles are resolved and the backend binary is outside the payload boundary · log, telemetry, hostname, workloadmeta: their mocks, since no assertion reads them |

**Validated:** setting `DD_LOGS_ENABLED` to an invalid value in the `env_types` fixture failed the `env_types` subtest on an unexpected `/logs_enabled` issue · after the restore, the snapshot matched · the 9 subtests passed on the rerun · lint clean
**Not covered:** a real secret backend binary, the systemd environment file setting `DD_*` variables, and the packaged Agent's hostname and version. No other level covers them; keeping one new-e2e scenario as a smoke test would
```

The first version of this test was a new-e2e suite provisioning an EC2 host per run. It became `comp/healthplatform/invalidconfig_payload_integration_test.go` ([#60753](https://github.com/DataDog/datadog-agent/pull/60753), not yet on `main` when this skill was written): the same 9 scenarios and golden reports, in about 4 seconds, failing on the same regressions.
