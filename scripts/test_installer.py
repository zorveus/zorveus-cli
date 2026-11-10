"""Exercise installation in isolated directories, with no network requests."""

import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest


INSTALLER = Path(__file__).resolve().with_name("install.sh")


class InstallerTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="zorveus-installer-")
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.tools = self.root / "tools"
        self.fixtures = self.root / "fixtures"
        self.downloads = self.root / "downloads"
        self.home = self.root / "home"
        for directory in [self.tools, self.fixtures, self.downloads, self.home]:
            directory.mkdir()

        # Only these local tools are visible. The downloader never reaches a network.
        for name in ["cat", "mktemp", "rm", "mkdir", "cp", "chmod", "mv", "awk", "grep"]:
            path = shutil.which(name)
            self.assertIsNotNone(path, name)
            (self.tools / name).symlink_to(path)
        self.write_tool("uname", """#!/bin/sh
case "$1" in
  -s) printf '%s\\n' "$TEST_OS" ;;
  -m) printf '%s\\n' "$TEST_ARCH" ;;
  *) exit 1 ;;
esac
""")
        self.write_tool("curl", f"#!{sys.executable}\n" + """
import os
from pathlib import Path
import sys
args = sys.argv[1:]
assert args[args.index('--proto') + 1] == '=https'
assert args[args.index('--proto-redir') + 1] == '=https'
url = args[-1]
assert url.startswith('https://zorveus.com/cli/')
with open(os.environ['TEST_LOG'], 'a') as log:
    log.write(url + '\\n')
if os.environ.get('TEST_FAIL_DOWNLOAD') == url.rsplit('/', 1)[-1]:
    sys.exit(22)
source = Path(os.environ['TEST_FIXTURES']) / url.rsplit('/', 1)[-1]
if not source.is_file():
    sys.exit(22)
Path(args[args.index('--output') + 1]).write_bytes(source.read_bytes())
""")
        self.checksum_tool = f"#!{sys.executable}\n" + """
import hashlib
from pathlib import Path
import sys
path = Path(sys.argv[-1])
print(hashlib.sha256(path.read_bytes()).hexdigest() + '  ' + str(path))
"""
        self.write_tool("sha256sum", self.checksum_tool)
        self.payload = b"test release binary; this file must never be executed\n"
        checksum = hashlib.sha256(self.payload).hexdigest()
        artifacts = [f"zorveus-{system}-{arch}" for system in ["linux", "darwin"]
                     for arch in ["amd64", "arm64"]]
        for name in artifacts:
            (self.fixtures / name).write_bytes(self.payload)
        (self.fixtures / "SHA256SUMS").write_text(
            "".join(f"{checksum}  {name}\n" for name in artifacts))
        (self.fixtures / "latest").write_text("v1.2.3\n")
        self.environment = {
            "PATH": str(self.tools),
            "HOME": str(self.home),
            "TMPDIR": str(self.downloads),
            "TEST_OS": "Linux",
            "TEST_ARCH": "x86_64",
            "TEST_FIXTURES": str(self.fixtures),
            "TEST_LOG": str(self.root / "requests.log"),
        }

    def write_tool(self, name, content):
        path = self.tools / name
        path.write_text(content)
        path.chmod(0o755)

    def enable_completion_fixture(self, shell):
        self.environment["SHELL"] = "/bin/" + shell
        self.environment["TEST_COMPLETION_LOG"] = str(self.root / "completion.log")
        self.payload = b'''#!/bin/sh
[ "$1" = completion ] || exit 1
printf '%s\\n' "$2" >> "$TEST_COMPLETION_LOG"
[ "${TEST_FAIL_COMPLETION:-}" != yes ] || exit 1
case "$2" in
  zsh) printf '%s\\n' '#compdef zorveus' '_zorveus() { return 0; }' 'compdef _zorveus zorveus' ;;
  bash) printf '%s\\n' 'complete -W "login logout" zorveus' ;;
  fish) printf '%s\\n' 'complete -c zorveus -f -a "login logout"' ;;
  *) exit 1 ;;
esac
'''
        checksum = hashlib.sha256(self.payload).hexdigest()
        artifacts = [f"zorveus-{system}-{arch}" for system in ["linux", "darwin"]
                     for arch in ["amd64", "arm64"]]
        for name in artifacts:
            (self.fixtures / name).write_bytes(self.payload)
        (self.fixtures / "SHA256SUMS").write_text(
            "".join(f"{checksum}  {name}\n" for name in artifacts))

    def run_installer(self, *arguments, succeeds=True):
        result = subprocess.run(["/bin/sh", str(INSTALLER), *arguments],
                                env=self.environment, capture_output=True, text=True)
        if succeeds:
            self.assertEqual(result.returncode, 0, result.stderr)
        else:
            self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertEqual(list(self.downloads.iterdir()), [], "temporary downloads remain")
        self.assertEqual(list(self.home.rglob(".zorveus.*")), [], "staged binary remains")
        self.assertEqual(list(self.home.rglob(".zorveus-completion.*")), [],
                         "staged completion remains")
        return result

    def request_log(self):
        path = self.root / "requests.log"
        return path.read_text() if path.exists() else ""

    def test_supported_platforms(self):
        for system, arch, artifact in [
            ("Linux", "x86_64", "linux-amd64"),
            ("Linux", "amd64", "linux-amd64"),
            ("Linux", "aarch64", "linux-arm64"),
            ("Darwin", "x86_64", "darwin-amd64"),
            ("Darwin", "arm64", "darwin-arm64"),
        ]:
            with self.subTest(system=system, arch=arch):
                self.environment.update(TEST_OS=system, TEST_ARCH=arch)
                self.run_installer()
                installed = self.home / ".local/bin/zorveus"
                self.assertEqual(installed.read_bytes(), self.payload)
                self.assertEqual(installed.stat().st_mode & 0o777, 0o755)
                self.assertIn("/v1.2.3/zorveus-" + artifact, self.request_log())

    def test_pinned_release_and_directory_with_spaces(self):
        destination = self.home / "custom tools"
        self.run_installer("--version", "v2.0.0-rc.1", "--bin-dir", str(destination))
        self.assertEqual((destination / "zorveus").read_bytes(), self.payload)
        self.assertNotIn("/latest", self.request_log())
        self.assertIn("/v2.0.0-rc.1/", self.request_log())

    def test_macos_checksum_fallback(self):
        (self.tools / "sha256sum").unlink()
        self.write_tool("shasum", self.checksum_tool)
        self.environment.update(TEST_OS="Darwin", TEST_ARCH="arm64")
        self.run_installer()

    def test_rejected_download_preserves_existing_installation(self):
        destination = self.home / ".local/bin"
        destination.mkdir(parents=True)
        installed = destination / "zorveus"
        installed.write_bytes(b"previous version")
        for failure in ["checksum", "download", "missing_checksum", "duplicate_checksum"]:
            with self.subTest(failure=failure):
                checksum = hashlib.sha256(self.payload).hexdigest()
                self.environment.pop("TEST_FAIL_DOWNLOAD", None)
                if failure == "checksum":
                    checksum = "0" * 64
                if failure == "download":
                    self.environment["TEST_FAIL_DOWNLOAD"] = "zorveus-linux-amd64"
                content = f"{checksum}  zorveus-linux-amd64\n"
                if failure == "missing_checksum":
                    content = f"{checksum}  zorveus-linux-arm64\n"
                if failure == "duplicate_checksum":
                    content += content
                (self.fixtures / "SHA256SUMS").write_text(content)
                self.run_installer(succeeds=False)
                self.assertEqual(installed.read_bytes(), b"previous version")

    def test_failure_during_replacement_preserves_previous_binary(self):
        destination = self.home / ".local/bin"
        destination.mkdir(parents=True)
        installed = destination / "zorveus"
        installed.write_bytes(b"previous version")
        (self.tools / "mv").unlink()
        self.write_tool("mv", "#!/bin/sh\nexit 1\n")
        self.run_installer(succeeds=False)
        self.assertEqual(installed.read_bytes(), b"previous version")

    def test_platform_and_argument_errors_do_not_download(self):
        for system, arch, arguments in [
            ("FreeBSD", "x86_64", []),
            ("Linux", "armv7l", []),
            ("Linux", "x86_64", ["--version", "../../bad"]),
            ("Linux", "x86_64", ["--version", "v1.2.3\nv4.5.6"]),
            ("Linux", "x86_64", ["--version"]),
            ("Linux", "x86_64", ["--bin-dir", "relative/path"]),
            ("Linux", "x86_64", ["--unexpected"]),
            ("Linux", "x86_64", ["--shell"]),
            ("Linux", "x86_64", ["--shell", "unsupported"]),
        ]:
            with self.subTest(system=system, arch=arch, arguments=arguments):
                self.environment.update(TEST_OS=system, TEST_ARCH=arch)
                self.run_installer(*arguments, succeeds=False)
                self.assertEqual(self.request_log(), "")

    def test_missing_dependencies_fail_before_download(self):
        for name in ["sha256sum", "curl"]:
            (self.tools / name).unlink()
            result = self.run_installer(succeeds=False)
            self.assertIn("Install", result.stderr)
            self.assertEqual(self.request_log(), "")

    def test_help_needs_no_download(self):
        result = self.run_installer("--help")
        self.assertIn("--bin-dir", result.stdout)
        self.assertEqual(self.request_log(), "")

    def test_zsh_completion_preserves_startup_file_and_does_not_duplicate(self):
        self.enable_completion_fixture("zsh")
        startup = self.home / ".zshrc"
        original = "# My shell settings\nexport MY_SETTING=kept\n"
        startup.write_text(original)
        self.run_installer()
        self.run_installer()
        content = startup.read_text()
        self.assertTrue(content.startswith(original))
        self.assertEqual(content.count("# >>> Zorveus completion >>>"), 1)
        completion = self.home / ".local/share/zorveus/completions/zorveus.zsh"
        self.assertIn("compdef _zorveus zorveus", completion.read_text())
        zsh = shutil.which("zsh")
        if zsh:
            environment = self.environment | {"PATH": os.defpath}
            result = subprocess.run([zsh, "-fc", '. "$HOME/.zshrc"; print -r -- "${_comps[zorveus]}"'],
                                    env=environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout.strip(), "_zorveus")

    def test_completion_is_configured_when_installer_is_piped_to_sh(self):
        self.enable_completion_fixture("zsh")
        result = subprocess.run(["/bin/sh"], input=INSTALLER.read_text(),
                                env=self.environment, capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Completion configured for zsh", result.stdout)
        self.assertIn("# >>> Zorveus completion >>>", (self.home / ".zshrc").read_text())

    def test_completion_respects_custom_directories_and_startup_symlinks(self):
        self.enable_completion_fixture("zsh")
        zsh_directory = self.home / "shell's config"
        zsh_directory.mkdir()
        existing = self.home / "existing-shell-config"
        existing.write_text("# Existing config\n")
        startup = zsh_directory / ".zshrc"
        startup.symlink_to(existing)
        data_directory = self.home / "data's $(literal)"
        self.environment.update(ZDOTDIR=str(zsh_directory), XDG_DATA_HOME=str(data_directory))
        self.run_installer()
        self.assertTrue(startup.is_symlink())
        self.assertTrue(existing.read_text().startswith("# Existing config\n"))
        self.assertIn("# >>> Zorveus completion >>>", existing.read_text())
        self.assertTrue((data_directory / "zorveus/completions/zorveus.zsh").is_file())
        self.assertFalse((self.home / ".zshrc").exists())

    def test_bash_completion_registers_login_and_interactive_shells(self):
        self.enable_completion_fixture("bash")
        profile = self.home / ".profile"
        profile.write_text("# Existing login config\n")
        self.run_installer()
        self.run_installer()
        for startup in [profile, self.home / ".bashrc"]:
            self.assertEqual(startup.read_text().count("# >>> Zorveus completion >>>"), 1)
        self.assertFalse((self.home / ".bash_profile").exists())
        self.assertTrue((self.home / ".local/share/bash-completion/completions/zorveus").is_file())
        bash = shutil.which("bash")
        if bash:
            # Stand in for an already-loaded bash-completion package.
            script = '_get_comp_words_by_ref() { :; }; . "$HOME/.bashrc"; complete -p zorveus'
            result = subprocess.run([bash, "--noprofile", "--norc", "-c", script],
                                    env=self.environment, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("zorveus", result.stdout)

    def test_fish_uses_its_completion_directory_without_startup_edits(self):
        self.enable_completion_fixture("fish")
        config_directory = self.home / "custom config"
        self.environment["XDG_CONFIG_HOME"] = str(config_directory)
        self.run_installer()
        completion = config_directory / "fish/completions/zorveus.fish"
        self.assertIn("complete -c zorveus", completion.read_text())
        self.assertFalse((config_directory / "fish/config.fish").exists())
        self.assertFalse((self.home / ".zshrc").exists())

    def test_completion_can_be_disabled_or_shell_overridden(self):
        self.enable_completion_fixture("zsh")
        self.run_installer("--no-completion")
        self.assertFalse((self.home / ".zshrc").exists())
        self.assertFalse((self.root / "completion.log").exists())
        self.run_installer("--shell", "fish")
        self.assertTrue((self.home / ".config/fish/completions/zorveus.fish").is_file())
        self.assertFalse((self.home / ".zshrc").exists())

    def test_completion_failure_keeps_binary_and_existing_configuration(self):
        self.enable_completion_fixture("zsh")
        startup = self.home / ".zshrc"
        original = "# Existing settings\n"
        startup.write_text(original)
        self.environment["TEST_FAIL_COMPLETION"] = "yes"
        result = self.run_installer()
        self.assertIn("shell completion was not configured", result.stderr)
        self.assertEqual(startup.read_text(), original)
        self.assertEqual((self.home / ".local/bin/zorveus").read_bytes(), self.payload)

    def test_invalid_download_is_never_executed_for_completion(self):
        self.enable_completion_fixture("zsh")
        (self.fixtures / "zorveus-linux-amd64").write_bytes(b"unverified executable")
        self.run_installer(succeeds=False)
        self.assertFalse((self.root / "completion.log").exists())
        self.assertFalse((self.home / ".zshrc").exists())


if __name__ == "__main__":
    unittest.main()
