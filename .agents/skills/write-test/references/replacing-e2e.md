# Replacing an existing new-e2e test

When the request is to replace a suite with something cheaper, its assertions are the behavior. The procedure moves the assertions a cheaper test can hold, and keeps the others in the suite, with their CI coverage intact.

## Audit the suite by its path

Invoke `e2e-audit` with the suite's concrete path rather than a description of it: its procedure for existing tests follows the suite's setup, helpers and provisioners, which hold coverage the test bodies don't show (a service restart in setup, an install in the provisioner), and returns a verdict per assertion. Split the verdicts as Step 2 of the skill does, with one difference: an assertion that still needs E2E stays in the suite as it is, and goes to `write-e2e` only when the audit says the suite lacks coverage it needs.

## Move only what a validated test replaces

Write and validate the lower-level tests (Steps 3 to 5). Remove an assertion from the suite only once the test replacing it is validated. Keep in the suite any assertion still uncertain, or whose replacement isn't validated, and report it as kept with the reason. Ask before removing a suite another team owns (`.github/CODEOWNERS`).

## Shrink the suite and its wiring

Shrink the suite to the assertions that still need E2E, or remove it when none do. Remove what only served the removed tests too: fixtures, helpers, then rerun gazelle.

CI selects suites by package and by name: a job's `TARGETS` lists packages, and its `EXTRA_PARAMS` (`--run TestECSSuite`) or the template it extends can narrow them to some suites. Several jobs can share one package: `new-e2e-containers-ecs` and `new-e2e-containers` both target `./tests/containers` and run different suites. So for each job whose `TARGETS` holds the package (`grep -rn 'TARGETS:.*<package>' .gitlab/`):

- When a suite it selects is still in the package, leave its `TARGETS` alone, and narrow its `--run` filter only if it names the removed suite.
- When none is, remove the package from its `TARGETS`, and remove the job and its `.gitlab/JOBOWNERS` entry when no target is left.

## Check what's left

Removing a test, a helper or a fixture can break the shared setup a kept assertion relies on. Lint the shrunk suite (`dda inv linter.go --module=test/new-e2e --targets=./tests/<area>`), and run its kept assertions with `write-e2e`'s Step 9 G4, which provisions cloud infrastructure: show the commands and get agreement first. When that run doesn't happen, report the kept assertions as **not re-run**.

Report what became of each assertion under **Replaced** (see the skill's Output).

## Example

**Input:** "Replace the agent-health e2e suite checking invalid-config payloads with something cheaper." This is the first version of the skill's main example, before [#60753](https://github.com/DataDog/datadog-agent/pull/60753) replaced it; the suite and file names are illustrative.

The audit, run on the suite's file under `test/new-e2e/tests/agent-health/`, classified the 9 payload scenarios as integration: every hop runs in-process, and a local fakeintake receives the same payload. It kept one assertion in E2E: that the packaged Agent, started by systemd with `DD_*` variables from its environment file, reports the issue, since the service manager is the boundary.

```markdown
**Replaced:** `TestInvalidConfigSuite` — the 9 payload scenarios moved to `TestInvalidConfigPayload/<scenario>` in `comp/healthplatform`, each validated · the systemd environment-file scenario kept in E2E: the service manager setting `DD_*` variables is the boundary · removed the 9 scenario methods and their fixtures; `new-e2e-agent-health` still runs the package's other suites and this one, so its `TARGETS` and JOBOWNERS entry stay · shrunk suite: lint clean, the kept scenario passed G4
```
