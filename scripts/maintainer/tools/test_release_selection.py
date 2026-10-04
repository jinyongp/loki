"""Exercise selection against the actual target-specific Go and recipe closures."""
import copy
from functools import lru_cache
import unittest
from unittest.mock import patch

import plan_release


class RealClosureSelectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        original=plan_release.go_closure
        cls.cached=staticmethod(lru_cache(None)(lambda packages,system,arch: original(list(packages),system,arch)))
        cls.closure_patch=patch.object(plan_release,"go_closure",side_effect=lambda packages,system,arch: cls.cached(tuple(packages),system,arch))
        cls.closure_patch.start()
        cls.addClassCleanup(cls.closure_patch.stop)
        cls.current,_=plan_release.fingerprints("0.2.4")
        cls.baseline={"schema":2,"contract":plan_release.CONTRACT,"release":"0.2.4","units":{key:dict(value,version="0.2.4",producer={"source_commit":"a"*40},acceptance={"passed":True}) for key,value in cls.current.items()}}

    def changed(self,path):
        original=plan_release.Inputs.files
        def files(instance,paths):
            result=original(instance,paths)
            if path in result:
                result[path]=["100644","changed-fixture"]
            return result
        with patch.object(plan_release.Inputs,"files",files):
            current,_=plan_release.fingerprints("0.2.4")
        return plan_release.select("0.2.4",current,copy.deepcopy(self.baseline))

    def test_cli_help_has_no_browser_or_full_build_or_checks(self):
        plan=self.changed("cmd/loki-manager/help.go")
        self.assertEqual(len(plan["matrices"]["manager"]),6)
        for key in ("browser","full","browser-checks","full-checks"):
            self.assertEqual(plan["matrices"][key],[],key)

    def test_windows_installer_selects_only_windows_managers(self):
        plan=self.changed("scripts/maintainer/tools/install.ps1")
        self.assertEqual({row["os"] for row in plan["matrices"]["manager"]},{"windows"})
        self.assertEqual(len(plan["matrices"]["manager"]),2)
        self.assertEqual(plan["matrices"]["browser"],[])
        self.assertEqual(plan["matrices"]["full"],[])

    def test_one_browser_input_selects_one_native_browser(self):
        plan=self.changed("packaging/tools/inputs/browser-linux-amd64-project-host.json")
        self.assertEqual(len(plan["matrices"]["browser"]),1)
        self.assertEqual(plan["matrices"]["browser"][0]["arch"],"amd64")
        self.assertEqual(plan["matrices"]["manager"],[])
        self.assertEqual(plan["matrices"]["full"],[])

    def test_git_payload_selects_only_owned_modules_and_roles(self):
        plan=self.changed("packaging/tools/inputs/git-packages-linux-amd64.json")
        self.assertEqual(plan["matrices"]["manager"],[])
        self.assertEqual(plan["matrices"]["browser"],[])
        self.assertEqual(len(plan["matrices"]["full"]),1)
        self.assertEqual(plan["matrices"]["full"][0]["modules"],["git"])
        self.assertEqual(plan["matrices"]["full"][0]["roles"],["git-workload"])

    def test_worker_change_selects_all_dependent_image_roles(self):
        plan=self.changed("cmd/loki/main.go")
        self.assertEqual(plan["matrices"]["manager"],[])
        self.assertEqual(plan["matrices"]["browser"],[])
        self.assertEqual(len(plan["matrices"]["full"]),2)
        for row in plan["matrices"]["full"]:
            self.assertEqual(set(row["roles"]),set(plan_release.ROLES))


if __name__=="__main__":
    unittest.main()
