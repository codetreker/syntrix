# Agent Note: Compare Benchmark Result Artifacts

Status: proposed

## Problem

The [benchmark guide](../../../../docs/benchmark/README.md) advertises
`syntrix-benchmark compare results/run1.json results/run2.json`. The
[command dispatcher](../../../../packages/syntrix/cmd/syntrix-benchmark/main.go) implements only
run, version, and help. The [design](../../../../docs/benchmark/DESIGN.md) calls
for throughput deltas, percentile latency changes, error-rate changes, and
side-by-side charts. There is no command connecting stored results to that
analysis; this finding is based on source inspection, not an executed comparison.

## Proposal

Implement comparison of one explicit baseline and one or more candidate JSON
reports. Validate the report schema, units, scenario, operation mix, data shape,
worker count, rate settings, and measurement phases before calculating deltas.
Show different target identifiers and build provenance explicitly without
requiring them to match: changing the deployment is a legitimate comparison.
Incompatible workload configurations must prevent a regression verdict and
produce a precise diagnostic; an explicitly requested descriptive comparison
may still show the differing runs side by side.

Report total and per-operation throughput, latency percentiles, and error rates.
For zero baselines, show absolute change and an unavailable percentage instead
of division-by-zero values. Produce console and machine-readable summaries and
the design's optional self-contained HTML comparison with charts. Keep numerical
regression thresholds explicit and opt-in, with distinct exit statuses for a
threshold violation and invalid input. A single pair of runs must not be
presented as statistically significant evidence.

## Alternatives

**External scripts or spreadsheets.** They offer flexible analysis but require
each operator to reimplement schema checks, units, and failure semantics.
They remain useful for exploratory analysis of the exported reports.

**A persistent benchmark history service.** It could support repeated-run
statistics and trends, but adds storage and service operations unrelated to
comparing local artifacts. Reconsider when cross-run history is required.

## Acceptance Criteria

- Known baseline/candidate fixtures produce exact expected absolute and
  percentage deltas, including zero baselines and missing operation classes.
- Malformed, unsupported-version, incomplete, or incompatible inputs fail with
  the affected file and reason; no regression verdict is emitted for them.
- Explicit thresholds distinguish passing results, regressions, and input errors
  through documented exit statuses.
- Console, JSON, and HTML present the same metrics and identify configuration
  differences; HTML works without external assets or services.

## Risks

Environmental variance can resemble regressions. Preserve provenance and sample
counts, and describe threshold results as numerical comparisons. Escape report
strings in HTML and exclude sensitive configuration from all output.

## Dependencies

[Report export](2026-09-07-benchmark-report-export.md) owns versioned input
artifacts; [detailed results](../bug-fix/2026-09-07-benchmark-detailed-results.md)
owns the metric completeness and measurement metadata they contain.
