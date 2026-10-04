"""Selection, verified cache restoration and publication cohort regression checks."""
import copy
import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import input_cache
from plan_release import select, CONTRACT
from release_pipeline import combine, download, write, validate_source, native
from release_gate import gate, evidence


def fixture():
    target = {"os":"linux", "arch":"amd64", "mode":"project-host"}
    current = {"manager:linux:amd64": {"kind":"manager", "target":target, "runner":"ubuntu-24.04", "fingerprint":"a"*64, "validation":"b"*64, "bridge":"c"*64}}
    # select's supported-target iteration is fixed; make a complete small input map.
    from plan_release import TARGETS, ROLES, OWNERS, CONFIG
    for system, arch, runner in TARGETS:
        t = dict(target, os=system, arch=arch)
        current[f"manager:{system}:{arch}"] = dict(current["manager:linux:amd64"], target=t, runner=runner)
        if (system, arch) != ("windows", "arm64"):
            current[f"module:browser:{system}:{arch}:project-host"] = {"kind":"module", "owner":"browser", "target":t, "runner":runner, "fingerprint":"d"*64, "validation":"e"*64}
        if system == "linux":
            for owner in CONFIG["modules"]:
                current[f"module:{owner}:linux:{arch}:full"] = {"kind":"module", "owner":owner, "target":dict(t,mode="full"), "runner":runner, "fingerprint":"f"*64, "validation":"g"*64}
            for role in ROLES:
                current[f"image:{role}:linux:{arch}"] = {"kind":"image", "owner":OWNERS[role], "role":role, "target":dict(t,mode="full"), "runner":runner, "fingerprint":"h"*64, "validation":"full-v2"}
    units = {k:dict(v,version="0.2.4",producer={"source_commit":"a"*40},acceptance={"passed":True}) for k,v in current.items()}
    return current,{"schema":2,"contract":CONTRACT,"release":"0.2.4","units":units}


class SelectionTests(unittest.TestCase):
    def test_windows_native_architecture_case_is_normalized(self):
        with patch("release_pipeline.platform.system",return_value="Windows"),patch("release_pipeline.platform.machine",return_value="ARM64"):
            self.assertEqual(native(),("windows","arm64"))

    def test_go_dependency_json_has_explicit_utf8_on_windows(self):
        import plan_release
        with patch.object(plan_release.subprocess,"check_output",return_value="{}") as execute:
            # A standard-library-only fixture avoids module bookkeeping.
            execute.return_value='{"Standard": true}'
            plan_release.go_closure(["./cmd/loki-manager"],"windows","amd64")
            self.assertEqual(execute.call_args.kwargs["encoding"],"utf-8")

    def test_actual_plan_maps_python_test_change_to_source_checks(self):
        import plan_release
        current, old=fixture()
        old["source_commit"]="a"*40
        inputs=plan_release.Inputs()
        # Simulate the producer-to-HEAD diff while exercising actual path mapping.
        original=plan_release.subprocess.check_output
        def changed(command, *args, **kwargs):
            if command[:3]==["git","diff","--name-only"]:
                return "scripts/maintainer/tools/test_bundle_inputs.py\n"
            return original(command,*args,**kwargs)
        with patch.object(plan_release,"fingerprints",return_value=(current,inputs)),patch.object(plan_release.subprocess,"check_output",side_effect=changed):
            result=plan_release.plan("0.2.4",old)
        for matrix in ("manager","browser","full"):
            self.assertEqual(result["matrices"][matrix],[])

    def test_locally_validated_source_cannot_change_during_dispatch(self):
        validate_source("a"*40,"a"*40)
        validate_source("","a"*40)
        with self.assertRaises(ValueError):validate_source("a"*40,"b"*40)

    def test_first_release_builds_all_targets(self):
        current,_=fixture();result=select("0.2.4",current)
        self.assertEqual([len(result["matrices"][k]) for k in ("manager","browser","full")],[6,5,2])

    def test_cli_version_only_preserves_tools_and_images(self):
        current,old=fixture();result=select("0.2.5",current,old)
        self.assertEqual(len(result["matrices"]["manager"]),6)
        for key in ("browser","full","browser-checks","full-checks"):
            self.assertEqual(result["matrices"][key],[])
        self.assertEqual(result["units"]["module:browser:linux:amd64:project-host"]["version"],"0.2.4")

    def test_test_only_change_validates_without_building(self):
        current,old=fixture();current["module:browser:linux:amd64:project-host"]["validation"]="new-tests"
        result=select("0.2.4",current,old)
        self.assertEqual(result["matrices"]["browser"],[])
        self.assertEqual(len(result["matrices"]["browser-checks"]),1)

    def test_unknown_contract_or_incomplete_baseline_rejected(self):
        current,old=fixture();old["contract"]="other"
        with self.assertRaises(ValueError):select("0.2.4",current,old)
        _,old=fixture();old["units"].pop(next(iter(old["units"])))
        with self.assertRaises(ValueError):select("0.2.4",current,old)

    def test_reuse_requires_original_acceptance(self):
        current,old=fixture();p=select("0.2.4",current,old)
        p["installer_fingerprint"]="fixture"
        with tempfile.TemporaryDirectory() as temp:
            combine(p,Path(temp))
            p["units"]["manager:linux:amd64"]["previous"].pop("acceptance")
            with self.assertRaises(ValueError):combine(p,Path(temp))

    def test_owned_download_digest_cannot_be_substituted(self):
        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/"archive";path.write_bytes(b"changed")
            record={"url":"https://github.com/jinyongp/loki/releases/download/v0.2.4/fixture.zip","bytes":7,"sha256":"a"*64}
            with self.assertRaises(ValueError):download(record,path)


class CacheTests(unittest.TestCase):
    def test_corrupt_restore_cold_fallback_and_verified_hit(self):
        with tempfile.TemporaryDirectory() as temp,patch.dict(os.environ,{"LOKI_INPUT_CACHE":temp}):
            root=Path(temp);source=root/"source";source.write_bytes(b"trusted")
            expected={"bytes":7,"sha256":hashlib.sha256(b"trusted").hexdigest()}
            (root/expected["sha256"]).write_bytes(b"corrupt")
            self.assertFalse(input_cache.restore(root/"restored",expected))
            input_cache.save(source,expected)
            self.assertTrue(input_cache.restore(root/"restored",expected))
            self.assertEqual((root/"restored").read_bytes(),b"trusted")

    def test_cache_permission_failure_is_optional(self):
        expected={"bytes":7,"sha256":"a"*64}
        with patch.object(input_cache,"location",side_effect=PermissionError("fixture")):
            self.assertFalse(input_cache.restore(Path("unused"),expected))

    def test_partial_restore_does_not_block_exclusive_cold_download(self):
        with tempfile.TemporaryDirectory() as temp,patch.dict(os.environ,{"LOKI_INPUT_CACHE":temp}):
            root=Path(temp);expected={"bytes":7,"sha256":hashlib.sha256(b"trusted").hexdigest()}
            (root/expected["sha256"]).write_bytes(b"trusted")
            destination=root/"download"
            def interrupted(source,target):
                target.write_bytes(b"part")
                raise OSError("fixture interrupted copy")
            with patch.object(input_cache.shutil,"copyfile",side_effect=interrupted):
                self.assertFalse(input_cache.restore(destination,expected))
            with destination.open("xb") as stream:stream.write(b"trusted")
            self.assertTrue(input_cache.valid(destination,expected))


class GateTests(unittest.TestCase):
    def test_same_run_previous_attempt_proofs_bind_exact_lock(self):
        current,old=fixture();p=select("0.2.5",current,old)
        p["installer_fingerprint"]="fixture"
        with tempfile.TemporaryDirectory() as temp,patch.dict(os.environ,{"GITHUB_RUN_ID":"42","GITHUB_RUN_ATTEMPT":"2"}):
            root=Path(temp);lock={"release":p["release"],"source_commit":p["source_commit"],"units":{k:dict(v) for k,v in old["units"].items()}}
            for k,u in p["units"].items():
                if u["action"]=="build":lock["units"][k]=dict(u,producer={"source_commit":p["source_commit"]})
            write(root/"assets/loki-release-lock.json",lock)
            from release_pipeline import digest
            exact=digest(root/"assets/loki-release-lock.json")
            for t in p["matrices"]["manager"]:
                proof=evidence(p,"manager",t["os"],t["arch"],exact);proof["run_attempt"]="1"
                write(root/"checks"/t["os"]/t["arch"]/"acceptance.json",proof)
            results={name:"success" for name in ("plan","source-checks","assemble","manager-build","manager-checks")}
            results.update({name:"skipped" for name in ("browser-build","full-build","browser-checks","full-checks")})
            actual=gate(p,root,root/"checks",results)
            self.assertEqual(actual["units"]["manager:linux:amd64"]["acceptance"]["run_attempt"],"1")
            # The same proof cannot authorize a changed publication lock.
            with self.assertRaisesRegex(ValueError,"cohort"):
                gate(p,root,root/"checks",results)

    def test_selected_failure_and_missing_native_proof_rejected(self):
        current,_=fixture();p=select("0.2.4",current)
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)
            with self.assertRaisesRegex(ValueError,"did not all succeed"):
                gate(p,root,root,{"plan":"success"})

    def test_cancelled_unselected_job_is_not_success(self):
        current,old=fixture();p=select("0.2.4",current,old)
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(ValueError,"cancelled"):
                gate(p,Path(temp),Path(temp),{"manager-build":"cancelled"})


if __name__=="__main__":unittest.main()
