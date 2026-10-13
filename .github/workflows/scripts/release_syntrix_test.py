#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("release_syntrix.sh").resolve()
WORKSPACE = SCRIPT.parents[3]
DIGEST = "sha256:" + "a" * 64


class ReleasePipelineTest(unittest.TestCase):
    def setUp(self):
        temporary = WORKSPACE / ".tmp"
        temporary.mkdir(exist_ok=True)
        self.fixture = tempfile.TemporaryDirectory(prefix="release-test-", dir=temporary)
        self.addCleanup(self.fixture.cleanup)
        self.root = Path(self.fixture.name)
        self.remote = self.root / "origin.git"
        self.repo = self.root / "source"
        self.repo.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.fixture_env = {
            **os.environ,
            "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1",
            "GIT_AUTHOR_NAME": "Release Fixture",
            "GIT_AUTHOR_EMAIL": "release-fixture@example.invalid",
            "GIT_COMMITTER_NAME": "Release Fixture",
            "GIT_COMMITTER_EMAIL": "release-fixture@example.invalid",
        }
        self.run_command("git", "init", "--bare", "--quiet", str(self.remote))
        self.git("init", "--quiet", "--initial-branch=main")
        self.git("remote", "add", "origin", str(self.remote))
        deployment = self.repo / "packages/syntrix/tools/deployment-cli/deployment"
        deployment.mkdir(parents=True)
        (deployment / "README.md").write_text(
            "[Dockerfile](../../../Dockerfile)\n"
            "[Storage](../../../../../docs/storage.md#bootstrap)\n"
            "[Renderer](render.go)\n"
            "[Template](standalone/kubernetes.template.yml)\n"
        )
        (deployment / "RELEASE.md").write_text(
            "[Deployment](README.md)\n[Workflow](../../../../../.github/workflows/syntrix-release.yml)\n"
        )
        (deployment / "render.go").write_text("package deployment\n")
        for mode in ("standalone", "distributed"):
            (deployment / mode).mkdir()
            (deployment / mode / "values.example.yml").write_text("image: syntrix:development\n")
            (deployment / mode / "kubernetes.template.yml").write_text("apiVersion: v1\n")
        self.git("add", ".")
        self.git("commit", "--quiet", "--message=Release fixture")
        self.sha = self.git("rev-parse", "HEAD").strip()
        self.git("push", "--quiet", "origin", "main")
        self.env_file = self.root / "github-env"
        self.env_file.touch()
        self.env = {
            **self.fixture_env,
            "PATH": str(self.bin) + os.pathsep + os.environ["PATH"],
            "GITHUB_EVENT_NAME": "workflow_dispatch",
            "GITHUB_REF": "refs/heads/main",
            "GITHUB_SHA": self.sha,
            "GITHUB_ENV": str(self.env_file),
            "GITHUB_REPOSITORY": "codetreker/syntrix",
            "GITHUB_SERVER_URL": "https://github.com",
            "GITHUB_RUN_ID": "123",
            "GITHUB_RUN_ATTEMPT": "2",
            "SYNTRIX_IMAGE_REPOSITORY": "ghcr.io/codetreker/syntrix-server",
            "SYNTRIX_VERSION": "261013.1",
            "SYNTRIX_RELEASE_DATE": "2026-10-13",
            "SYNTRIX_BUILT_AT": "2026-10-13T23:59:59Z",
            "SYNTRIX_IMAGE_DIGEST": DIGEST,
        }
        self.executable("date", '#!/bin/sh\ncase "$*" in\n  *%y%m%d*) printf "261013\\n" ;;\n  *) printf "2026-10-13T23:59:59Z\\n" ;;\nesac\n')

    def run_command(self, *args, check=True, env=None):
        return subprocess.run(args, cwd=self.repo, env=env if env is not None else self.fixture_env, check=check,
                              text=True, capture_output=True, timeout=30)

    def git(self, *args):
        return self.run_command("git", *args).stdout

    def executable(self, name, text):
        path = self.bin / name
        path.write_text(text)
        path.chmod(0o755)

    def release(self, operation, check=True):
        return self.run_command("bash", str(SCRIPT), operation, env=self.env, check=check)

    def contract(self):
        return {
            "managed_runtime_contract": 1,
            "version": self.env["SYNTRIX_VERSION"],
            "git_commit": self.sha,
            "modes": ["standalone", "distributed"],
            "render_phases": ["bootstrap", "acceptance", "full"],
        }

    def prepare_clis(self, output=None):
        if output is None:
            output = json.dumps(self.contract()) + "\n"
        self.env["FIXTURE_CLI_CALLS"] = str(self.root / "cli-calls")
        source = (
            "#!/usr/bin/env python3\nimport json\nimport os\nfrom pathlib import Path\nimport sys\n"
            "if sys.argv[1:] == ['deployment-contract']:\n"
            f"    sys.stdout.write({output!r})\n"
            "else:\n"
            "    with Path(os.environ['FIXTURE_CLI_CALLS']).open('a') as calls:\n"
            "        calls.write(json.dumps(sys.argv[1:]) + '\\n')\n"
        )
        for arch in ("amd64", "arm64"):
            cli = self.repo / f".tmp/release/bin/linux-{arch}/deployment-cli"
            cli.parent.mkdir(parents=True, exist_ok=True)
            cli.write_text(source)
            cli.chmod(0o755)

    def test_versions_use_numeric_remote_tags_and_frozen_utc_date(self):
        self.release("prepare")
        self.assertIn("SYNTRIX_VERSION=261013.1\n", self.env_file.read_text())
        for tag in ("syntrix-261013.2", "syntrix-261013.10", "syntrix-261013.9",
                    "syntrix-261012.999", "syntrix-261013.invalid", "syntrix-261013.20.extra"):
            self.git("tag", tag)
            self.git("push", "--quiet", "origin", "refs/tags/" + tag)
        self.git("tag", "syntrix-261013.500")
        self.env_file.write_text("")
        self.release("prepare")
        self.assertEqual(self.env_file.read_text(),
                         "SYNTRIX_VERSION=261013.11\nSYNTRIX_RELEASE_DATE=2026-10-13\n"
                         "SYNTRIX_BUILT_AT=2026-10-13T23:59:59Z\n")

    def test_rejects_other_sources_and_tags_exact_commit(self):
        for key, value in (("GITHUB_EVENT_NAME", "push"), ("GITHUB_REF", "refs/heads/feature"),
                           ("GITHUB_SHA", "b" * 40)):
            original = self.env[key]
            self.env[key] = value
            self.assertNotEqual(self.release("prepare", check=False).returncode, 0)
            self.env[key] = original
        self.assertEqual(self.env_file.read_text(), "")
        self.release("tag")
        remote_tag = self.git("ls-remote", "--refs", "origin", "refs/tags/syntrix-261013.1")
        self.assertEqual(remote_tag.split()[0], self.sha)
        self.assertNotEqual(self.release("tag", check=False).returncode, 0)

    def test_same_commit_remote_tag_is_not_a_new_reservation(self):
        self.run_command("git", "--git-dir=" + str(self.remote), "update-ref",
                         "refs/tags/syntrix-261013.1", self.sha)
        result = self.release("tag", check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("did not create a new remote tag", result.stderr)
        self.assertIn("[up to date]", result.stdout)
        remote_tag = self.git("ls-remote", "--refs", "origin", "refs/tags/syntrix-261013.1")
        self.assertEqual(remote_tag.split()[0], self.sha)

    def test_bundle_uses_committed_files_and_pins_image_and_document_links(self):
        release_dir = self.repo / ".tmp/release"
        self.prepare_clis()
        deployment = self.repo / "packages/syntrix/tools/deployment-cli/deployment"
        (deployment / "standalone/values.example.yml").write_text("image: working-tree-only\n")
        (deployment / "untracked-secret").write_text("must not ship\n")
        self.release("bundle")
        archive = release_dir / "syntrix-261013.1.tar.gz"
        with tarfile.open(archive) as packaged:
            prefix = "syntrix-261013.1/"
            self.assertNotIn(prefix + "deployment/untracked-secret", packaged.getnames())
            self.assertNotIn(prefix + "deployment/render.go", packaged.getnames())
            metadata = json.load(packaged.extractfile(prefix + "meta.json"))
            self.assertIs(type(metadata["managed_runtime_contract"]), int)
            self.assertEqual(metadata["managed_runtime_contract"], 1)
            self.assertEqual(metadata["git_commit"], self.sha)
            self.assertEqual(metadata["git_tag"], "syntrix-261013.1")
            self.assertEqual(metadata["image"]["digest"], DIGEST)
            self.assertEqual(metadata["image"]["platforms"], ["linux/amd64", "linux/arm64"])
            source = f"https://github.com/codetreker/syntrix/blob/{self.sha}/"
            readme = packaged.extractfile(prefix + "deployment/README.md").read().decode()
            self.assertIn(source + "packages/syntrix/Dockerfile", readme)
            self.assertIn(source + "docs/storage.md#bootstrap", readme)
            self.assertIn(source + "packages/syntrix/tools/deployment-cli/deployment/render.go", readme)
            self.assertIn("[Template](standalone/kubernetes.template.yml)", readme)
            root_readme = packaged.extractfile(prefix + "README.md").read().decode()
            self.assertIn("[Deployment](deployment/README.md)", root_readme)
            self.assertIn(source + ".github/workflows/syntrix-release.yml", root_readme)
            for mode in ("standalone", "distributed"):
                values = packaged.extractfile(prefix + f"deployment/{mode}/values.example.yml").read().decode()
                self.assertEqual(values, f"image: ghcr.io/codetreker/syntrix-server:261013.1@{DIGEST}\n")
            for arch in ("amd64", "arm64"):
                self.assertEqual(packaged.getmember(prefix + f"bin/linux-{arch}/deployment-cli").mode & 0o111, 0o111)
        calls = [json.loads(line) for line in Path(self.env["FIXTURE_CLI_CALLS"]).read_text().splitlines()]
        self.assertEqual(len(calls), 6)
        for mode in ("standalone", "distributed"):
            matching = [call for call in calls if f".tmp/release/{mode}" in call[-1]]
            self.assertEqual(len(matching), 3)
            self.assertEqual(sum("--bootstrap" in call and "--writes-quiesced" in call for call in matching), 1)
            self.assertEqual(sum("--acceptance-only" in call for call in matching), 1)

    def test_bundle_rejects_invalid_or_mismatched_contract_before_rendering(self):
        invalid = []
        for key, value in (
            ("managed_runtime_contract", None), ("managed_runtime_contract", True),
            ("managed_runtime_contract", 1.0), ("managed_runtime_contract", 2),
            ("version", "development"), ("git_commit", "b" * 40),
            ("modes", ["standalone"]), ("render_phases", ["bootstrap", "full"]),
        ):
            contract = self.contract()
            if value is None:
                del contract[key]
            else:
                contract[key] = value
            invalid.append(json.dumps(contract))
        invalid.extend(("not-json", "[]", "{}\n{}", " " * 4097))
        for output in invalid:
            with self.subTest(output=output[:100]):
                self.prepare_clis(output)
                result = self.release("bundle", check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(Path(self.env["FIXTURE_CLI_CALLS"]).exists())
                self.assertFalse((self.repo / ".tmp/release/syntrix-261013.1.tar.gz").exists())

    def test_build_cli_injects_matching_identity_for_both_architectures(self):
        calls = self.root / "go-calls"
        self.env["FIXTURE_GO_CALLS"] = str(calls)
        self.executable("go", """#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
with Path(os.environ['FIXTURE_GO_CALLS']).open('a') as calls:
    calls.write(json.dumps({'arguments': sys.argv[1:], 'os': os.environ['GOOS'],
                            'architecture': os.environ['GOARCH'], 'cgo': os.environ['CGO_ENABLED']}) + '\\n')
""")
        self.release("build-cli")
        builds = [json.loads(line) for line in calls.read_text().splitlines()]
        self.assertEqual([build["architecture"] for build in builds], ["amd64", "arm64"])
        for build in builds:
            self.assertEqual(build["os"], "linux")
            self.assertEqual(build["cgo"], "0")
            arguments = build["arguments"]
            self.assertEqual(arguments[arguments.index("-ldflags") + 1],
                             f"-X main.version=261013.1 -X main.gitCommit={self.sha}")
            self.assertEqual(arguments[-1], "./tools/deployment-cli/cmd/deployment-cli")
            self.assertEqual(arguments[arguments.index("-o") + 1],
                             f"../../.tmp/release/bin/linux-{build['architecture']}/deployment-cli")

    def test_push_requires_public_access_and_both_platforms(self):
        release_dir = self.repo / ".tmp/release"
        release_dir.mkdir(parents=True)
        self.executable("docker", "#!/bin/sh\nexit 0\n")
        self.executable("curl", """#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys
args = sys.argv[1:]
if args[-1] == 'https://ghcr.io/token':
    print(json.dumps({'token': 'anonymous-token'}))
elif os.environ['FIXTURE_ACCESS'] == 'private':
    sys.exit(22)
else:
    platforms = [{'os': 'linux', 'architecture': 'amd64'}]
    if os.environ['FIXTURE_ACCESS'] == 'public':
        platforms.append({'os': 'linux', 'architecture': 'arm64'})
    Path(args[args.index('--output') + 1]).write_text(json.dumps({'manifests': [{'platform': p} for p in platforms]}))
    Path(args[args.index('--dump-header') + 1]).write_bytes(('HTTP/2 200\\r\\nDocker-Content-Digest: sha256:' + 'a' * 64 + '\\r\\n').encode())
""")
        self.env["FIXTURE_ACCESS"] = "private"
        failure = self.release("push-image", check=False)
        self.assertNotEqual(failure.returncode, 0)
        self.assertIn("Change visibility → Public", failure.stderr)
        self.assertIn("https://github.com/orgs/codetreker/packages/container/syntrix-server/settings", failure.stderr)
        self.assertEqual(self.env_file.read_text(), "")
        self.env["FIXTURE_ACCESS"] = "missing-arm64"
        self.assertNotEqual(self.release("push-image", check=False).returncode, 0)
        self.assertEqual(self.env_file.read_text(), "")
        self.env["FIXTURE_ACCESS"] = "public"
        self.release("push-image")
        self.assertEqual(self.env_file.read_text(), f"SYNTRIX_IMAGE_DIGEST={DIGEST}\n")

    def test_release_stays_draft_when_upload_fails(self):
        calls = self.root / "gh-calls"
        self.env["FIXTURE_GH_CALLS"] = str(calls)
        self.executable("gh", """#!/usr/bin/env python3
import os
from pathlib import Path
import sys
with Path(os.environ['FIXTURE_GH_CALLS']).open('a') as calls:
    calls.write(' '.join(sys.argv[1:]) + '\\n')
if sys.argv[2] == 'upload' and os.environ['FIXTURE_UPLOAD'] == 'fail':
    sys.exit(1)
""")
        self.env["FIXTURE_UPLOAD"] = "fail"
        self.assertNotEqual(self.release("release", check=False).returncode, 0)
        self.assertIn("--verify-tag --draft", calls.read_text())
        self.assertNotIn("release edit", calls.read_text())
        calls.write_text("")
        self.env["FIXTURE_UPLOAD"] = "success"
        self.release("release")
        commands = calls.read_text().splitlines()
        self.assertEqual(len(commands), 3)
        self.assertIn("--draft=false", commands[-1])


if __name__ == "__main__":
    unittest.main()
