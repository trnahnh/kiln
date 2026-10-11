# Postmortem: the nightly pipeline was red for 29 days and nobody noticed

**Date of writing:** 2026-10-10. **Status:** fix committed on the branch, unverified until a `main` run.
**Severity:** no user impact; a four-week gap in the only automated proof the platform has.

Blameless: the question is which conditions let a harness bug sit unseen for a month, not who wrote it.

## Summary

The Phase 8 end-to-end test (`TestPhase8Outbox`, `e2e/phase8_test.go`) was pushed on 2026-09-12 without having run anywhere, because the author's machine had no Docker and the test can only run against a bootstrapped cluster. Its precondition check was wrong for the burst it drives, so it failed on the push-triggered run and on every one of the 28 nightly runs that followed, through 2026-10-10. The failure was found during an audit of the project's claims, from `gh run list`, not from any alert. For those 29 days the `platform-e2e` job was red, the README badge showed it, and the durable-outbox exit criterion stood unproven while [`ROADMAP.md`](../ROADMAP.md) and [`TESTING.md`](../TESTING.md) described its proof as if it ran.

## Timeline (UTC)

| When | What |
|---|---|
| 2026-09-12 21:29 | Commits `70af22a..cc6c5f0` (Phase 8, durable audit outbox) pushed to `main`. `SESSION.md` records "Vetted with the e2e tag; runs in CI on push (no local Docker)" and "result recorded below when it lands". No entry followed. |
| 2026-09-12 22:42 | Push-triggered run [34720086857](https://github.com/trnahnh/kiln/actions/runs/34720086857): ten jobs green, `platform-e2e` fails in `TestPhase8Outbox` at `phase8_test.go:275`: "kiln-operator-controller-manager must have been killed without grace several times, got 2". The burst subtests had all passed in 146 s. |
| 2026-09-13 to 2026-10-10 | The 04:00 UTC nightly runs 28 more times. Every run: ten jobs green, `platform-e2e` red at the same line with the same count. |
| 2026-10-10 ~21:00 | Audit of the repository's claims lists recent runs and finds the streak. Root cause read from the log and the test within ten minutes. |
| 2026-10-10 | Fix committed on `enhance/evidence-depth` (`d4967a1`); CI runs only on `main` pushes and the nightly, so it is unverified at the time of writing. |

## Root cause

The test kills the three controllers on a 40-second cadence, alternating a grace-0 delete with an ordinary one, starting with grace 0, for as long as the burst of three parallel subtests runs. When the burst ends it stops the killer and asserts that every controller was killed without grace at least three times and gracefully at least twice. That needs the burst to outlast the fifth kill round, 160 seconds. The burst finished in 137 to 146 seconds on every run, so the hard kills landed at 0 and 80 seconds, two of the three required, and the assertion failed before the proof itself, the comparison of informer-observed transitions with audit rows, ever ran.

The assertion was written as a guard that the kills happened at all. It encoded an assumption about the burst's duration that was never measured, because the test had never been run.

## What made it last

- **No feedback loop on the nightly.** A scheduled run's failure produces no notification by default. The only signals were the README badge and the Actions tab, and neither was looked at between pushes.
- **The push run was not watched to completion.** The 66-minute `platform-e2e` job finished after the working session ended. The log's promise to record its result was not kept, and nothing reminded anyone of it.
- **One job carries eight phases.** `platform-e2e` runs `TestPhase2` through `TestPhase8` in one `go test` invocation. A red job says "something in e2e failed", which after one unresolved failure means nothing: a second, real regression in Phases 2 to 7 would have produced the same red badge and been just as invisible.
- **The docs ran ahead of the proof.** `TESTING.md` described the Phase 8 proof as running in CI, and `ROADMAP.md` lists its exit criterion, on the day of the push. The documentation of the test was indistinguishable from evidence that it passed.

## Impact

- No cluster, user or data impact; this is a test harness.
- Phase 8's exit criterion has not been demonstrated by CI. The reconciler-level proofs (each controller's envtest suite drains the outbox through a recorder) did run and pass on every run; the end-to-end claim, exactly-one row per transition under repeated SIGKILL, remains open until a green `main` run.
- 29 wasted runs of a 66-minute job.

## What went right

- The failure was deterministic and the log named the line, the count and the expectation. Diagnosis took minutes once anyone looked.
- Every other job stayed green throughout; the unit, integration and envtest layers were doing their job.

## Fix

`d4967a1`: the killer keeps running after the burst until every controller has met both minimums, bounded at six cadences, and only then stops. The assertion that used to fail is now the wait's condition. The proof subtests are untouched.

## Action items

| # | Action | Status |
|---|---|---|
| 1 | Merge the fix to `main` and confirm the next nightly's `platform-e2e` is green, including the "every controller comes back and every outbox drains" subtest, which has never yet run in CI. | Open |
| 2 | Notify on a scheduled-run failure: a final job in `ci.yaml` that opens (or comments on) a GitHub issue when the nightly fails, so a red nightly is a visible event rather than a badge colour. | Implemented on this branch as the `nightly-alert` job in `ci.yaml`; unverified until a nightly runs it |
| 3 | Never leave a push run unwatched: a new end-to-end test is not "vetted" until its first run is read. Record the run id and the result in `SESSION.md` in the same entry as the push. | Process; adopted |
| 4 | Split `platform-e2e` into one step per phase (or `-run` per phase with `continue-on-error` and a summary), so one failing phase cannot hide another. | Open; proposed |
| 5 | Phrase a proof's precondition as a wait, not an assertion, whenever the precondition is a timing the test itself controls. | Adopted in the fix |
