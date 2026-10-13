# Syntrix Releases

The manually triggered **Syntrix Release** workflow publishes the instance runtime
from the main commit selected when the workflow is triggered. It delivers a public
multi-platform image and a version-matched Kubernetes deployment archive. The
[deployment guide](README.md) owns backend prerequisites, values, bootstrap,
runtime acceptance, and recovery. The [release decision](../../../.agents/notes/implemented/process/2026-10-13-manual-syntrix-release.md)
records version allocation and publication trade-offs.

## Trigger and publication

Open the repository's Actions page, select **Syntrix Release**, and run it on
`main`. Other branches and tags are rejected. The workflow checks out the exact
trigger commit; later main changes do not affect that run. Runs share one
concurrency group and queue rather than canceling an active release.

The version is `YYMMDD.sequence`, using the UTC date selected at the start of the
run. The sequence starts at 1 and is one greater than the numeric maximum among
remote tags matching `syntrix-YYMMDD.sequence` for that date. For example,
`261013.10` follows `261013.9`. The Git tag is `syntrix-261013.10`; the image is
`ghcr.io/codetreker/syntrix-server:261013.10`.

Publication proceeds in this order:

1. Start the existing MongoDB replica set, PostgreSQL, and NATS test services.
   Run `make -C packages/syntrix test`, which executes the Go module's unit and
   integration tests without race or coverage instrumentation. Compile Linux
   amd64/arm64 CLIs and build both runtime image platforms. The container builder
   cross-compiles natively; it does not require CPU emulation.
2. Create the remote Git tag at the tested commit. Creation must succeed before
   uploading the versioned image; an existing tag is never replaced.
3. Push the multi-platform image, verify anonymous access and both target
   platforms, and obtain the registry index digest.
4. Generate `meta.json` and the deployment archive from the same checkout. Render
   both modes with the bundled native CLI to validate the staged templates and
   example values.
5. Create a draft GitHub Release for the existing tag, upload the archive, then
   publish it. A successful run links the image and release in its job summary.

The workflow uses `GITHUB_TOKEN` with `contents: write` and `packages: write`.
Repository rules must permit this workflow to create the release tags, publish
the package, and create releases. An existing package must grant this repository
Actions access.

## Public package setup

GitHub creates a new GHCR package as private. Repository linkage inherits access
permissions but does not change package visibility, and GitHub provides no
supported REST or GraphQL operation to change that visibility.

After the first upload, open the package's **Package settings**, select **Change
visibility** in **Danger Zone**, and choose **Public**. This is a one-time setting
for `syntrix-server`. The workflow checks the uploaded index through an anonymous
registry token, so authenticated push success alone cannot satisfy publication.
If the first run fails this check, make the package public and run the workflow
again; that run uses the next sequence. GitHub documents the setting in
[package access and visibility](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility).

## Archive contents and metadata

The GitHub Release attachment is `syntrix-VERSION.tar.gz`. Its top-level directory
is `syntrix-VERSION`:

```text
syntrix-VERSION/
├── README.md
├── meta.json
├── bin/
│   ├── linux-amd64/syntrix-cli
│   └── linux-arm64/syntrix-cli
└── deployment/
    ├── README.md
    ├── RELEASE.md
    ├── standalone/
    │   ├── kubernetes.template.yml
    │   └── values.example.yml
    └── distributed/
        ├── kubernetes.template.yml
        └── values.example.yml
```

The archive includes tracked deployment files from the release commit. Example
values reference `ghcr.io/codetreker/syntrix-server:VERSION@sha256:DIGEST`, pinning
the uploaded multi-platform index. Operator credentials and generated manifests
are not bundled. Links to repository files outside the archive resolve to the
release commit on GitHub.

`meta.json` records `version`, `release_date`, `built_at` (the build-start timestamp in UTC RFC 3339),
`git_tag`, the full `git_commit`, `workflow_run_url`, and the integer
`workflow_run_attempt`. Its `image` object
records `repository`, `tag`, `digest`, and the `linux/amd64` and `linux/arm64`
`platforms`. The digest identifies the multi-platform registry index, rather than
a local image ID or a single platform's manifest.

## Use an extracted archive

Extract the attachment and enter its top-level directory. On a Linux operator
machine, select the CLI matching that machine's CPU; this can differ from the
cluster's CPUs:

```bash
case "$(uname -m)" in
  x86_64) SYNTRIX_CLI=./bin/linux-amd64/syntrix-cli ;;
  aarch64|arm64) SYNTRIX_CLI=./bin/linux-arm64/syntrix-cli ;;
  *) printf 'Unsupported Linux operator architecture\n' >&2; exit 1 ;;
esac
SYNTRIX_DEPLOYMENT=./deployment
mkdir -p .tmp
SYNTRIX_MODE=standalone
SYNTRIX_TEMPLATE="$SYNTRIX_DEPLOYMENT/$SYNTRIX_MODE/kubernetes.template.yml"
SYNTRIX_VALUES=.tmp/syntrix-values.yml
cp "$SYNTRIX_DEPLOYMENT/$SYNTRIX_MODE/values.example.yml" "$SYNTRIX_VALUES"
chmod 600 "$SYNTRIX_VALUES"
```

Choose `distributed` before copying if required. Replace example connections,
credentials, origins, names, storage, and resources with operator-selected
values. Follow the [deployment guide](README.md), retaining the CLI and deployment
variables selected above. Fresh claims require its explicit bootstrap procedure;
a successful template render does not establish live backend or runtime readiness.
The public image can be pulled without GHCR credentials.

## Failures and reruns

Before remote tag creation, a failed run leaves its candidate version unreserved.
Once tag creation succeeds, that version is consumed. Later failures preserve any
tag, image, or draft already created; the workflow does not roll back publication
or overwrite a version. Rerun the complete workflow to allocate the next available
sequence. Gaps in the sequence are expected.

Git tags, registry uploads, and GitHub Releases are separate remote operations.
If a request's response fails, inspect the job log and remote objects: the remote
operation may already have succeeded, including final Release publication. Local
test-service cleanup runs independently of publication success.
