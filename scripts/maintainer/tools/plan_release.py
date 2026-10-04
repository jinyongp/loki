"""Select release jobs from immutable published inputs and real source closures."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess

from release_config import CONFIG, CONTRACT, ROOT, version

TARGETS = [
    ("linux", "amd64", "ubuntu-24.04"), ("linux", "arm64", "ubuntu-24.04-arm"),
    ("windows", "amd64", "windows-2025"), ("windows", "arm64", "windows-11-arm"),
    ("darwin", "amd64", "macos-15-intel"), ("darwin", "arm64", "macos-15"),
]
ROLES = {"service": ["runtime-core"], "gateway": ["runtime-core"],
         "workload": ["runtime-core", "execution"],
         "git-workload": ["runtime-core", "execution", "git"], "browser": ["runtime-core"]}
OWNERS = {"service": "runtime-core", "gateway": "runtime-core", "workload": "execution", "git-workload": "git", "browser": "browser"}
GO_PROGRAMS = {"runtime-core": ["./cmd/loki"], "execution": ["./cmd/launcher", "./cmd/executor"]}
SCRIPTS = "scripts/maintainer/tools/"


def checksum(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def go_closure(packages, system, arch):
    env = dict(os.environ, GOOS=system, GOARCH=arch, CGO_ENABLED="0", GOTOOLCHAIN="local")
    env.pop("GOFLAGS", None)
    raw = subprocess.check_output(["go", "list", "-mod=readonly", "-buildvcs=false", "-deps", "-json", *packages], cwd=ROOT, env=env, text=True)
    decoder, files, modules = json.JSONDecoder(), set(), {}
    sums = {}
    for line in (ROOT / "go.sum").read_text().splitlines():
        name, release, digest = line.split()
        sums[(name, release)] = digest
    while raw.strip():
        package, offset = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[offset:]
        if package.get("Standard"):
            continue
        module = package.get("Module", {})
        if module.get("Main"):
            directory = Path(package["Dir"])
            for field in ("GoFiles", "CgoFiles", "IgnoredGoFiles", "EmbedFiles", "CFiles", "CXXFiles", "HFiles", "SFiles", "SysoFiles"):
                for name in package.get(field, []):
                    files.add((directory / name).relative_to(ROOT).as_posix())
        else:
            if module.get("Replace") or not module.get("Version"):
                raise ValueError("release closure requires unmodified pinned Go modules")
            key = (module["Path"], module["Version"])
            modules["@".join(key)] = [sums[key], sums.get((key[0], key[1]+"/go.mod"))]
    return {"files": sorted(files), "modules": modules, "go": CONFIG["go"],
            "target": [system, arch], "cgo": "0", "tags": [], "buildvcs": False, "trimpath": True}


class Inputs:
    def __init__(self):
        self.known = set()
        raw = subprocess.check_output(["git", "ls-files", "--stage", "-z"], cwd=ROOT).decode()
        self.modes = {line.split("\t", 1)[1]: line.split()[0] for line in raw.split("\0") if line}

    def files(self, paths):
        found = {}
        for name in paths:
            path = ROOT / name
            if path.is_dir():
                found.update(self.files(p.relative_to(ROOT).as_posix() for p in path.rglob("*") if p.is_file() and "__pycache__" not in p.parts and p.suffix != ".pyc"))
            else:
                self.known.add(name)
                if path.is_symlink():
                    found[name] = ["symlink", os.readlink(path)]
                else:
                    found[name] = [self.modes.get(name, "100755" if path.exists() and path.stat().st_mode & 0o111 else "100644"),
                                   hashlib.sha256(path.read_bytes()).hexdigest() if path.exists() else "missing"]
        return found

    def digest(self, paths, extra=None):
        return checksum({"files": self.files(paths), "extra": extra})


def go_tests(closure):
    return sorted({p.relative_to(ROOT).as_posix() for name in closure["files"] for p in (ROOT/name).parent.glob("*_test.go")})


def fingerprints(release):
    inputs, units = Inputs(), {}
    common = [SCRIPTS+name for name in ("release_config.py", "input_cache.py", "plan_release.py", "release_pipeline.py")]+["LICENSE"]
    contracts = {"schema": 2, "composition": CONTRACT}
    for system, arch, runner in TARGETS:
        closure = go_closure(["./cmd/loki-manager"], system, arch)
        key = f"manager:{system}:{arch}"
        paths = common+closure["files"]+[SCRIPTS+"build_manager_bundle.py", SCRIPTS+("install.ps1" if system=="windows" else "install.sh")]
        bridge = [p for p in closure["files"] if p not in {"cmd/loki-manager/help.go", "cmd/loki-manager/upgrade.go"}]
        units[key] = {"fingerprint": inputs.digest(paths, {"closure": closure, "contract": contracts}),
                      "validation": inputs.digest(go_tests(closure)+[SCRIPTS+n for n in ("accept_manager_bundle.py", "accept_cli_upgrade.py", "accept_public_installer.py", "render_public_installers.py", "public-install.sh.tmpl", "public-install.ps1.tmpl", "release_gate.py")]),
                      "bridge": inputs.digest(bridge), "closure": closure, "kind": "manager", "target": {"os": system, "arch": arch, "mode": "project-host"}, "runner": runner}
        if (system, arch) != ("windows", "arm64"):
            key = f"module:browser:{system}:{arch}:project-host"
            paths = common+[SCRIPTS+n for n in ("build_browser_bundle.py", "acquire_browser_inputs.py")]+["package.json", "package-lock.json", "modules/browser/package.json", f"packaging/tools/inputs/browser-{system}-{arch}-project-host.json"]
            units[key] = {"fingerprint": inputs.digest(paths, contracts), "validation": inputs.digest([SCRIPTS+n for n in ("accept_browser_candidate.py", "accept_browser_capabilities.py", "accept_public_installer.py")]),
                          "kind": "module", "owner": "browser", "target": {"os": system, "arch": arch, "mode": "project-host"}, "runner": runner}
        if system != "linux":
            continue
        payloads = {}
        for owner in CONFIG["modules"]:
            if owner == "browser":
                continue
            paths = common+[SCRIPTS+n for n in ("build_full_bundle.py", "build_browser_bundle.py", "build_manager_bundle.py", "finalize_full_bundle.py")]
            paths += ["packaging/tools/module.full.json" if owner == "runtime-core" else f"modules/{owner}/module.full.json", f"packaging/tools/inputs/{owner}-linux-{arch}.json"]
            closure = go_closure(GO_PROGRAMS[owner], system, arch) if owner in GO_PROGRAMS else None
            if closure:
                paths += closure["files"]
            if owner in ("github", "coordination"):
                source = "gh" if owner == "github" else "devtools"
                paths += [f"packaging/tools/inputs/{source}-source-linux-{arch}.json"]+[SCRIPTS+n for n in ("acquire_go_vendor_inputs.py", "build_go_vendor.py")]
            if owner == "git":
                paths += ["modules/git/assets", f"packaging/tools/inputs/git-packages-linux-{arch}.json"]+[SCRIPTS+n for n in ("acquire_debian_inputs.py", "build_debian_closure.py")]
            if owner == "runtime-core":
                paths += ["packaging/tools/config", "packaging/tools/notices"]
            if owner == "execution":
                paths += ["packaging/native/toolchain-catalog.json"]
            if owner == "workspace":
                paths += ["bundled_skills"]
            payloads[owner] = {"fingerprint": inputs.digest(paths, {"closure": closure, "contract": contracts}), "closure": closure}
        images = {}
        recipe = json.loads((ROOT/f"packaging/tools/inputs/images-linux-{arch}.json").read_text())
        for role, dependencies in ROLES.items():
            paths = common+[SCRIPTS+"build_full_images.py", SCRIPTS+"prepare_candidate.py", SCRIPTS+"merge_catalogs.py", f"packaging/tools/inputs/trust-store-packages-linux-{arch}.json", SCRIPTS+"acquire_debian_inputs.py", SCRIPTS+"build_debian_closure.py"]
            if role == "browser":
                paths += [f"packaging/tools/inputs/browser-packages-linux-{arch}.json"]
            # Exclude publication destination/role list, which do not change a role's bytes.
            stable_recipe = {k:v for k,v in recipe.items() if k not in {"repository", "roles", "payloads"}}
            value = inputs.digest(paths, {"recipe": stable_recipe, "payloads": {p:payloads[p]["fingerprint"] for p in dependencies}, "role": role})
            images[role] = value
            units[f"image:{role}:linux:{arch}"] = {"fingerprint": value, "validation": "full-v2", "kind": "image", "owner": OWNERS[role], "role": role, "target": {"os":system,"arch":arch,"mode":"full"}, "runner":runner}
        for owner in CONFIG["modules"]:
            target = {"os":system,"arch":arch,"mode":"full"}
            payload = payloads.get(owner)
            paths = common+[SCRIPTS+"prepare_candidate.py", SCRIPTS+"merge_catalogs.py"]
            if owner == "browser":
                paths += [SCRIPTS+"build_browser_bundle.py", SCRIPTS+"acquire_browser_inputs.py", "package.json", "package-lock.json", "modules/browser/package.json", f"packaging/tools/inputs/browser-linux-{arch}-full.json"]
            owned_images = {role:value for role,value in images.items() if OWNERS[role] == owner}
            value = inputs.digest(paths, {"payload":payload, "images":owned_images, "contract":contracts})
            units[f"module:{owner}:linux:{arch}:full"] = {"fingerprint":value,"validation":inputs.digest([SCRIPTS+"accept_full_workspace_candidate.py"]), "payload":payload, "kind":"module","owner":owner,"target":target,"runner":runner}
    return units, inputs


def select(release, current, baseline=None, force=False):
    version(release)
    previous = baseline.get("units", {}) if baseline else {}
    if baseline and (baseline.get("schema") != 2 or baseline.get("contract") != CONTRACT or set(previous) != set(current)):
        raise ValueError("baseline release lock does not cover the supported composition")
    units = {}
    for key, source in current.items():
        old = previous.get(key)
        changed = force or old is None or old["fingerprint"] != source["fingerprint"]
        if source["kind"] == "manager" and old and old["version"] != release:
            changed = True
        item = dict(source, action="build" if changed else "reuse", version=release if changed else old["version"], reason="forced" if force else "first-baseline" if old is None else "inputs-or-version-changed" if changed else "verified-unchanged-inputs")
        if not changed:
            item["previous"] = old
        item["validate"] = changed or old is None or old.get("validation") != source["validation"]
        units[key] = item
    manager, browser, full, manager_checks, browser_checks, full_checks = [], [], [], [], [], []
    for system, arch, runner in TARGETS:
        target = {"os":system,"arch":arch,"runner":runner}
        key = f"manager:{system}:{arch}"
        if units[key]["action"] == "build":
            manager.append(target)
        if units[key]["validate"]:
            manager_checks.append(target)
        bridge_changed = not previous.get(key) or current[key]["bridge"] != previous[key].get("bridge")
        browser_key = f"module:browser:{system}:{arch}:project-host"
        if browser_key in units:
            if units[browser_key]["action"] == "build":
                browser.append(target)
            if units[browser_key]["validate"] or bridge_changed:
                browser_checks.append(target)
        if system == "linux":
            selected = [u["owner"] for k,u in units.items() if u["kind"]=="module" and u["target"]=={"os":system,"arch":arch,"mode":"full"} and u["action"]=="build"]
            roles = [u["role"] for u in units.values() if u["kind"]=="image" and u["target"]["arch"]==arch and u["action"]=="build"]
            if selected or roles:
                full.append(dict(target, modules=selected, roles=roles))
            if selected or roles or bridge_changed or any(u["validate"] for u in units.values() if u["kind"]=="module" and u["target"]=={"os":system,"arch":arch,"mode":"full"}):
                full_checks.append(target)
    return {"schema":2,"release":release,"contract":CONTRACT,"source_commit":subprocess.check_output(["git","rev-parse","HEAD"],cwd=ROOT,text=True).strip(),"units":units,
            "matrices":{"manager":manager,"browser":browser,"full":full,"manager-checks":manager_checks,"browser-checks":browser_checks,"full-checks":full_checks},"baseline_release":baseline.get("release") if baseline else None}


def plan(release, baseline=None, force=False):
    units, inputs = fingerprints(release)
    files = subprocess.check_output(["git","ls-files","-z"],cwd=ROOT).decode().split("\0")
    ignore_prefixes = ("docs/", ".github/", "scripts/verify/", "scripts/maintainer/preflight/", "scripts/build/", "tools/", "packaging/native/", "packaging/release-inputs/", "packaging/browser/", "packaging/systemd/", "packaging/host/", "plugins/")
    unknown = [p for p in files if p and p not in inputs.known and not p.startswith(ignore_prefixes) and not p.endswith((".md","_test.go")) and not (p.startswith(SCRIPTS) and Path(p).name.startswith("test_") and p.endswith(".py")) and p not in {"go.mod","go.sum","devtools.toml",".gitignore",".gitattributes","packaging/tools/release.json"}]
    # Detect unmapped changes against the producer revision, not the last push.
    if baseline:
        changed = subprocess.check_output(["git","diff","--name-only",baseline["source_commit"],"HEAD"],cwd=ROOT,text=True).splitlines()
        force = force or any(p in set(unknown)|set(baseline.get("unmapped_inputs", [])) for p in changed)
    result = select(release, units, baseline, force)
    result["unmapped_inputs"] = unknown
    result["installer_fingerprint"] = inputs.digest([SCRIPTS+"public-install.sh.tmpl", SCRIPTS+"public-install.ps1.tmpl", SCRIPTS+"render_public_installers.py"])
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True)
    parser.add_argument("--baseline", type=Path)
    parser.add_argument("--force", action="store_true")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    result = plan(args.version, json.loads(args.baseline.read_text()) if args.baseline else None, args.force)
    args.output.write_text(json.dumps(result, indent=2, sort_keys=True)+"\n")
