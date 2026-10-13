#!/usr/bin/env bash
set -euo pipefail

release_dir=.tmp/release

prepare() {
  test "$GITHUB_EVENT_NAME" = workflow_dispatch
  test "$GITHUB_REF" = refs/heads/main
  test "$(git rev-parse HEAD)" = "$GITHUB_SHA"
  mkdir -p "$release_dir"
  local built_at release_date day version
  built_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  release_date=${built_at:0:10}
  day=$(date -u -d "$release_date" +%y%m%d)
  git ls-remote --refs --tags origin "refs/tags/syntrix-$day.*" > "$release_dir/remote-tags"
  version=$(python3 - "$day" "$release_dir/remote-tags" <<'PY'
import re
import sys
from pathlib import Path

day, tags_file = sys.argv[1:]
pattern = re.compile(r"refs/tags/syntrix-" + day + r"\.([0-9]+)")
sequences = [int(match.group(1)) for line in Path(tags_file).read_text().splitlines()
             if (match := pattern.fullmatch(line.split()[1]))]
print(f"{day}.{max(sequences, default=0) + 1}")
PY
)
  printf 'SYNTRIX_VERSION=%s\nSYNTRIX_RELEASE_DATE=%s\nSYNTRIX_BUILT_AT=%s\n' \
    "$version" "$release_date" "$built_at" >> "$GITHUB_ENV"
  printf 'Allocated syntrix-%s at %s\n' "$version" "$GITHUB_SHA"
}

build_cli() {
  test -n "${SYNTRIX_VERSION:?expected release version}"
  test -n "${GITHUB_SHA:?expected release source commit}"
  local arch
  for arch in amd64 arm64; do
    mkdir -p "$release_dir/bin/linux-$arch"
    (
      cd packages/syntrix
      CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
        -ldflags "-X main.version=$SYNTRIX_VERSION -X main.gitCommit=$GITHUB_SHA" \
        -o "../../$release_dir/bin/linux-$arch/syntrix-cli" ./cmd/syntrix-cli
    )
  done
}

create_tag() {
  test "$(git rev-parse HEAD)" = "$GITHUB_SHA"
  local tag="syntrix-$SYNTRIX_VERSION"
  mkdir -p "$release_dir"
  git tag "$tag" "$GITHUB_SHA"
  git push --porcelain origin "refs/tags/$tag:refs/tags/$tag" | tee "$release_dir/tag-push"
  if ! awk -F '\t' -v ref="refs/tags/$tag:refs/tags/$tag" \
    '$1 == "*" && $2 == ref { created = 1 } END { exit !created }' "$release_dir/tag-push"; then
    printf 'Release tag reservation did not create a new remote tag: %s.\n' "$tag" >&2
    return 1
  fi
}

push_image() {
  local image="$SYNTRIX_IMAGE_REPOSITORY:$SYNTRIX_VERSION"
  local package="${SYNTRIX_IMAGE_REPOSITORY#ghcr.io/}" token digest
  docker push "$image"
  if ! token=$(curl --fail --silent --show-error --max-time 30 --get \
    --data-urlencode service=ghcr.io --data-urlencode "scope=repository:$package:pull" \
    https://ghcr.io/token | jq -er .token); then
    printf 'Could not obtain an anonymous GHCR pull token for %s. Set Package settings → Change visibility → Public at https://github.com/orgs/%s/packages/container/syntrix-server/settings, then start a fresh release run.\n' \
      "$image" "${package%%/*}" >&2
    return 1
  fi
  printf '::add-mask::%s\n' "$token"
  if ! curl --fail --silent --show-error --max-time 30 \
    --header "Authorization: Bearer $token" \
    --header 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json' \
    --dump-header "$release_dir/image-headers" \
    --output "$release_dir/image-manifest.json" \
    "https://ghcr.io/v2/$package/manifests/$SYNTRIX_VERSION"; then
    printf 'Anonymous pull failed for %s. Set Package settings → Change visibility → Public at https://github.com/orgs/%s/packages/container/syntrix-server/settings, then start a fresh release run.\n' \
      "$image" "${package%%/*}" >&2
    return 1
  fi
  jq -e '[.manifests[].platform | select(.os == "linux" and (.architecture == "amd64" or .architecture == "arm64")) | "\(.os)/\(.architecture)"] | sort == ["linux/amd64", "linux/arm64"]' \
    "$release_dir/image-manifest.json" > /dev/null
  digest=$(awk 'tolower($1) == "docker-content-digest:" {gsub(/\r/, "", $2); print $2}' "$release_dir/image-headers")
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
  printf 'SYNTRIX_IMAGE_DIGEST=%s\n' "$digest" >> "$GITHUB_ENV"
  printf 'Verified public multi-platform image %s@%s\n' "$image" "$digest"
}

bundle() {
  local root="$release_dir/syntrix-$SYNTRIX_VERSION" mode native_arch
  case "$(uname -m)" in
    x86_64) native_arch=amd64 ;;
    aarch64|arm64) native_arch=arm64 ;;
    *) printf 'Unsupported release packaging architecture.\n' >&2; return 1 ;;
  esac
  mkdir -p "$root/deployment"
  git archive "$GITHUB_SHA:packages/syntrix/deployment" README.md RELEASE.md standalone distributed \
    | tar -xf - -C "$root/deployment"
  cp -a "$release_dir/bin" "$root/bin"
  python3 - "$root" "$native_arch" <<'PY'
import json
import os
import posixpath
import re
import select
import subprocess
import sys
import time
from pathlib import Path
from urllib.parse import urlsplit

root = Path(sys.argv[1])
native_arch = sys.argv[2]
version = os.environ["SYNTRIX_VERSION"]
sha = os.environ["GITHUB_SHA"]
repository = os.environ["SYNTRIX_IMAGE_REPOSITORY"]
digest = os.environ["SYNTRIX_IMAGE_DIGEST"]
source_url = f'{os.environ["GITHUB_SERVER_URL"]}/{os.environ["GITHUB_REPOSITORY"]}/blob/{sha}/'
deployment_source = "packages/syntrix/deployment"

def read_contract():
    command = [str(root / f"bin/linux-{native_arch}/syntrix-cli"), "deployment-contract"]
    process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    deadline = time.monotonic() + 10
    output = bytearray()
    try:
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise ValueError("deployment contract command timed out")
            readable, _, _ = select.select([process.stdout], [], [], remaining)
            if not readable:
                raise ValueError("deployment contract command timed out")
            chunk = os.read(process.stdout.fileno(), 4097 - len(output))
            if not chunk:
                break
            output.extend(chunk)
            if len(output) > 4096:
                raise ValueError("deployment contract output exceeds 4096 bytes")
        if process.wait(timeout=max(0.001, deadline - time.monotonic())) != 0:
            raise ValueError("deployment contract command failed")
    finally:
        if process.poll() is None:
            process.kill()
        process.wait()
        process.stdout.close()
    contract = json.loads(output)
    if not isinstance(contract, dict):
        raise ValueError("deployment contract must be one JSON object")
    if type(contract.get("managed_runtime_contract")) is not int or contract["managed_runtime_contract"] != 1:
        raise ValueError("unsupported managed runtime contract")
    if contract.get("version") != version or contract.get("git_commit") != sha:
        raise ValueError("deployment contract does not match release identity")
    if contract.get("modes") != ["standalone", "distributed"]:
        raise ValueError("deployment contract does not support both modes")
    if contract.get("render_phases") != ["bootstrap", "acceptance", "full"]:
        raise ValueError("deployment contract does not support all render phases")
    return contract

contract = read_contract()

def rewrite_links(text, relative_file, relocated=False):
    def rewrite(match):
        destination = match.group(2)
        url = urlsplit(destination)
        if url.scheme or url.netloc or not url.path or destination.startswith("/"):
            return match.group(0)
        target = posixpath.normpath(posixpath.join(deployment_source, posixpath.dirname(relative_file), url.path))
        suffix = (f"?{url.query}" if url.query else "") + (f"#{url.fragment}" if url.fragment else "")
        packaged_target = root / "deployment" / posixpath.relpath(target, deployment_source)
        if not target.startswith(deployment_source + "/") or not packaged_target.exists():
            destination = source_url + target + suffix
        elif relocated:
            destination = "deployment/" + posixpath.relpath(target, deployment_source) + suffix
        return match.group(1) + destination + match.group(3)
    return re.sub(r"(\]\()([^\s)]+)(\))", rewrite, text)

release_guide = (root / "deployment/RELEASE.md").read_text()
for path in (root / "deployment").rglob("*.md"):
    path.write_text(rewrite_links(path.read_text(), path.relative_to(root / "deployment").as_posix()))
(root / "README.md").write_text(rewrite_links(release_guide, "RELEASE.md", relocated=True))
for mode in ("standalone", "distributed"):
    path = root / f"deployment/{mode}/values.example.yml"
    text, replacements = re.subn(r"^image: .*$", f"image: {repository}:{version}@{digest}", path.read_text(), flags=re.MULTILINE)
    if replacements != 1:
        raise ValueError(f"expected one image field in {path}")
    path.write_text(text)
metadata = {
    "managed_runtime_contract": contract["managed_runtime_contract"],
    "version": version,
    "release_date": os.environ["SYNTRIX_RELEASE_DATE"],
    "built_at": os.environ["SYNTRIX_BUILT_AT"],
    "git_tag": f"syntrix-{version}",
    "git_commit": sha,
    "image": {
        "repository": repository,
        "tag": version,
        "digest": digest,
        "platforms": ["linux/amd64", "linux/arm64"],
    },
    "workflow_run_url": f'{os.environ["GITHUB_SERVER_URL"]}/{os.environ["GITHUB_REPOSITORY"]}/actions/runs/{os.environ["GITHUB_RUN_ID"]}',
    "workflow_run_attempt": int(os.environ["GITHUB_RUN_ATTEMPT"]),
}
(root / "meta.json").write_text(json.dumps(metadata, indent=2) + "\n")
PY
  for mode in standalone distributed; do
    "$root/bin/linux-$native_arch/syntrix-cli" render-kubernetes \
      --template "$root/deployment/$mode/kubernetes.template.yml" \
      --config "$root/deployment/$mode/values.example.yml" \
      --output "$release_dir/$mode.yml"
    "$root/bin/linux-$native_arch/syntrix-cli" render-kubernetes \
      --template "$root/deployment/$mode/kubernetes.template.yml" \
      --config "$root/deployment/$mode/values.example.yml" \
      --bootstrap --writes-quiesced --output "$release_dir/$mode-bootstrap.yml"
    "$root/bin/linux-$native_arch/syntrix-cli" render-kubernetes \
      --template "$root/deployment/$mode/kubernetes.template.yml" \
      --config "$root/deployment/$mode/values.example.yml" \
      --acceptance-only --output "$release_dir/$mode-acceptance.yml"
  done
  tar -czf "$release_dir/syntrix-$SYNTRIX_VERSION.tar.gz" -C "$release_dir" "syntrix-$SYNTRIX_VERSION"
}

publish_release() {
  local tag="syntrix-$SYNTRIX_VERSION"
  gh release create "$tag" --repo "$GITHUB_REPOSITORY" --verify-tag \
    --draft --title "Syntrix $SYNTRIX_VERSION" --generate-notes
  gh release upload "$tag" "$release_dir/syntrix-$SYNTRIX_VERSION.tar.gz" --repo "$GITHUB_REPOSITORY"
  gh release edit "$tag" --repo "$GITHUB_REPOSITORY" --draft=false
}

case "${1:?expected prepare, build-cli, tag, push-image, bundle, or release}" in
  prepare) prepare ;;
  build-cli) build_cli ;;
  tag) create_tag ;;
  push-image) push_image ;;
  bundle) bundle ;;
  release) publish_release ;;
  *) printf 'Unknown release operation: %s\n' "$1" >&2; exit 1 ;;
esac
