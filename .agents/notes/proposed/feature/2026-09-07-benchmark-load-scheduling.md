# Agent Note: Honor Benchmark Warmup, Rampup, and Operation Rates

Status: proposed

## Problem

Warmup, Rampup, and operation Rate are represented in the
[configuration types](../../../../packages/syntrix/pkg/benchmark/types/types.go), but the
[runner](../../../../packages/syntrix/pkg/benchmark/runner/runner.go) immediately starts every
worker and stops accepting operations using `time.AfterFunc(config.Duration, ...)`.
Its loop applies no configured rate. The [design](../../../../docs/benchmark/DESIGN.md)
requires warmup exclusion and gradual load growth. Consequently, accepted settings
do not define the workload that static inspection shows being scheduled.

## Proposal

Make the runner own explicit setup, warmup, rampup, measured execution, drain,
and teardown phases. Duration should mean measured steady-load time; warmup and
rampup precede it and are excluded from reported measurement aggregates. Publish
phase durations and measured start/end timestamps so total wall time is clear.
Define omission separately from explicit zero for warmup and rampup, allowing
callers to disable either phase without a default silently restoring it.

Preserve the design's per-worker target-rate model: a positive operation Rate
caps that operation's starts per active worker; zero means unpaced execution.
Weights choose the eligible operation mix, while pacing prevents exceeding the
rate. Rampup increases active workers on a documented schedule and reports the
resulting aggregate target. Do not accumulate an unbounded queue of missed starts
or burst to recover missed deadlines; record offered and achieved rates and
scheduler delay. Stop admitting work at phase boundaries and use bounded draining
to keep warmup completions out of measured samples. Parent cancellation must
interrupt pacing waits and in-flight requests. Include all phases in CLI timeouts.

## Alternatives

**One aggregate rate across workers.** This keeps load constant as concurrency
changes, but changes the documented per-worker contract. Reconsider if capacity
experiments establish that aggregate rate is the intended user-facing unit.

**A fixed delay after each request.** It is easy to schedule, but request latency
changes the offered rate and cannot express the configured target accurately.

## Acceptance Criteria

- Controlled-clock tests observe the specified phases and worker ramp without
  including warmup or rampup completions in measured aggregates.
- Positive rates remain within the documented pacing tolerance; zero disables
  pacing, and invalid durations or rates fail before workload setup.
- Slow requests produce reported rate shortfall without queued bursts.
- Cancellation during every phase terminates within the drain deadline, and
  CLI deadlines cover the configured lifecycle.

## Risks

Pacing and metric resets can introduce timing bias. Publish the phase and
admission definitions with results; changing units later requires an explicit
configuration-format decision, not an implicit compatibility fallback.

## Dependencies

[Detailed results](../bug-fix/2026-09-07-benchmark-detailed-results.md) must consume
the runner's frozen measured interval rather than collector construction time.
