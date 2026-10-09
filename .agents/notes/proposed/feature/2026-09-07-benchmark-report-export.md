# Agent Note: Connect Benchmark Output Configuration to Report Export

Status: proposed

## Problem

The [configuration loader](../../../../packages/syntrix/pkg/benchmark/config/loader.go) accepts
JSON, Prometheus, and CSV output and resolves Output.File. The
[CLI](../../../../packages/syntrix/cmd/syntrix-benchmark/main.go) always creates a console reporter
and calls ReportSummary, without selecting a format or writing the configured
file. [ReportJSON](../../../../packages/syntrix/pkg/benchmark/reporter/console.go) already encodes
Result but is not called by the CLI. The loader also assigns
`config.Output.Console = true` whenever Console is false, preventing an explicit
request to disable console output. These are static integration findings.

## Proposal

Route the completed Result through the configured output destination and format.
Retain JSON encoding and implement the accepted CSV and Prometheus text
representations with documented units and escaping. JSON should be the complete,
versioned result artifact; tabular and metric formats should document which
fields they represent. Preserve human-readable console reporting when enabled,
while honoring explicit false through a configuration model that distinguishes
omission from false. Keep machine-readable stdout free of progress and diagnostics.

Define output-path template expansion, collision behavior, and relative-path
resolution before writing. Write file output to a sibling temporary file, close
it successfully, and rename it into place so interrupted writes cannot appear
as complete reports. Propagate encoding, write, and close failures with the
destination and run identifier; never report successful export after a partial
write. Sanitized effective configuration may be embedded, but credentials, key
contents, and raw document or HTTP error payloads must be excluded.

## Alternatives

**JSON-only output.** It would satisfy comparison and automation, but accepting
CSV and Prometheus while dropping them would narrow the current configuration
contract. Reconsider only with an explicit decision to remove those formats.

**Redirect human console output to a file.** This preserves visible summaries
but lacks a stable machine-readable schema and loses structured operation data.

## Acceptance Criteria

- Each accepted format produces a parseable artifact with documented units from
  one result; JSON includes a schema version and operation details.
- Console false survives configuration loading, and machine output contains no
  progress text, color escapes, or diagnostics.
- Unwritable destinations, encoding errors, and interrupted writes fail explicitly
  and leave no complete-looking partial report or overwritten collision.
- Export tests verify credential and payload exclusion.

## Risks

File templates can collide across simultaneous runs, and format changes can
break downstream analysis. Use run identifiers and explicit schema validation;
report conversion policy belongs to the format owner.

## Dependencies

[Detailed results](../bug-fix/2026-09-07-benchmark-detailed-results.md) owns the
complete source result. [Comparison](2026-09-07-benchmark-result-comparison.md)
consumes the versioned JSON artifact.
