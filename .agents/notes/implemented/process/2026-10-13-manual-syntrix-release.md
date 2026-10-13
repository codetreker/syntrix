# Agent Note: Manual Syntrix Release Publication

Status: implemented

## Problem

Operators need a tested runtime image and deployment templates that identify the
same source commit. The existing pull-request CI validates builds and runtime
contracts, but it does not allocate release versions, publish images, or attach
version-matched CLI and deployment files to GitHub Releases. Independent uploads
would make source identity and deployment compatibility difficult to establish.

## Decision

The [release workflow](../../../../.github/workflows/syntrix-release.yml) runs only
on manual dispatch for main and checks out the trigger SHA. Its fixed concurrency
group queues releases. A frozen UTC date and the numeric maximum among remote
`syntrix-YYMMDD.sequence` tags select the next candidate version. The remote tag is
the reservation boundary; external tag creation can still conflict and must fail
without replacing that tag.

The workflow reuses the existing Compose test infrastructure and non-race
`make test` command, cross-compiles Linux amd64/arm64 CLIs, and builds the runtime
image before creating its tag. The container builder runs on the build platform
and selects the target through `TARGETOS` and `TARGETARCH`. A containerd image
store permits loading both image platforms before publication. Docker setup
precedes Compose startup because changing stores hides containers from the
previous store. The [runtime-container decision](../architecture/2026-10-12-instance-container-kubernetes.md)
continues to own runtime hardening and Kubernetes generation.

The version tag points to the tested SHA, and the image uses
`ghcr.io/codetreker/syntrix-server:YYMMDD.sequence`. Anonymous registry access must
expose an index containing both platforms before a GitHub Release can be
published. Initial GHCR package visibility requires a one-time maintainer Public
setting: repository access inheritance does not inherit visibility, and the
official package APIs expose no visibility mutation.

The archive contains metadata, both statically linked Linux CLIs, deployment documentation/template trees, and extraction instructions. Staged example values pin the
published index digest. Repository links leaving the archive become SHA-pinned
source links. Packaging injects the same release version/full source SHA into both CLIs,
checks bounded `deployment-contract` JSON against the integer managed-runtime
marker and required modes/phases, and renders bootstrap/acceptance/full for both
modes using the bundled native CLI before uploading the archive to a draft Release and publishing it. The
[release guide](../../../../packages/syntrix/tools/deployment-cli/deployment/RELEASE.md) owns the
operational procedure and metadata fields. The
[managed-runtime decision](../architecture/2026-10-13-managed-runtime-contract.md)
owns capability version 1 and its runtime semantics; previously published
archives are unchanged.

## Alternatives

**Use the workflow run number as the sequence.** Run numbers span dates and count
runs independently of release tags. Remote tags provide the intended daily
numbering and include versions reserved by failed publication attempts.

**Build and push the multi-platform image in one step after reserving the tag.**
This would consume a version on compilation failure and violate the requested
build-before-tag order. Loading the complete index first preserves that order
without adding an OCI-copy tool.

**Ship only templates and metadata.** Go text templates require the matching
renderer. Including both Linux CLIs makes extraction sufficient for rendering
without a source checkout or Go compiler.

**Resume or roll back partial publication automatically.** Recovery would need
to reconcile uncertain remote success across tags, GHCR, and Releases, including
whether an image can safely be rebuilt under an existing version. Fresh version
allocation keeps failure handling explicit and avoids replacing prior objects.

## Consequences

A successful release ties tested source, a publicly accessible multi-platform
image, and deployment artifacts to one version. Runtime configuration,
credentials, signing material, cluster lifecycle, and bootstrap ownership remain
operator responsibilities.

Tag creation consumes the version even if later steps fail. Runs preserve partial
remote objects and full reruns allocate a new sequence; gaps are accepted. The
first package upload may fail the public-access gate until the maintainer changes
visibility. A failed response does not prove the corresponding remote operation
failed. Cross-service atomic publication is not promised.

The workflow retains its source SHA on rerun, while selecting a fresh UTC date
and available sequence. Test and build failures remain blocking. Pull-request
race and coverage gates retain their independent scope. The existing server CI
build job also checks the release helper against local Git, registry, and
Release fixtures without publishing remote artifacts.
