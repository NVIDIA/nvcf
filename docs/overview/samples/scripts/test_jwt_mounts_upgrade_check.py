# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
import copy
import json
from pathlib import Path
import runpy
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("check-upgrade-jwt-mounts.py")
CHECK = runpy.run_path(str(SCRIPT))["check"]
PLUGIN = "vault-plugin-secrets-jwt"
BEFORE = {"existing/jwt/": {"type": PLUGIN, "accessor": "synthetic-jwt-accessor",
                           "plugin_version": "", "running_plugin_version": "",
                           "running_sha256": "a" * 64}}
CATALOG = {"version": "v9.8.7", "command": f"{PLUGIN}-v9.8.7",
           "builtin": False, "sha256": "b" * 64}


class JwtMountChecks(unittest.TestCase):
    def test_unchanged_and_new_target_mount_pass(self):
        after = copy.deepcopy(BEFORE)
        after["new/jwt/"] = {"type": PLUGIN, "accessor": "synthetic-new-accessor",
                             "plugin_version": CATALOG["version"],
                             "running_plugin_version": CATALOG["version"],
                             "running_sha256": CATALOG["sha256"]}
        after["kv/"] = {"type": "kv"}
        CHECK(BEFORE)
        CHECK(BEFORE, after, CATALOG)

    def test_existing_mount_changes_are_rejected(self):
        for key, value in (("accessor", "replacement"), ("running_sha256", "c" * 64),
                           ("plugin_version", CATALOG["version"]),
                           ("running_plugin_version", CATALOG["version"]), ("type", "kv")):
            with self.subTest(field=key):
                after = copy.deepcopy(BEFORE)
                after["existing/jwt/"][key] = value
                with self.assertRaises(ValueError):
                    CHECK(BEFORE, after, CATALOG)
        with self.assertRaises(ValueError):
            CHECK(BEFORE, {"kv/": {"type": "kv"}}, CATALOG)
        after = copy.deepcopy(BEFORE)
        after["existing/jwt/"].update(plugin_version=CATALOG["version"],
                                       running_plugin_version=CATALOG["version"],
                                       running_sha256=CATALOG["sha256"])
        with self.assertRaises(ValueError):
            CHECK(BEFORE, after, CATALOG)

    def test_new_old_mount_is_rejected(self):
        after = copy.deepcopy(BEFORE)
        after["new/jwt/"] = dict(BEFORE["existing/jwt/"], accessor="new-accessor")
        with self.assertRaises(ValueError):
            CHECK(BEFORE, after, CATALOG)

    def test_incomplete_or_ambiguous_inventory_is_rejected(self):
        cases = [{}, {"path/": []}, {"path/": {"type": "plugin"}}]
        for field in ("accessor", "running_sha256"):
            case = copy.deepcopy(BEFORE)
            del case["existing/jwt/"][field]
            cases.append(case)
        for field, value in (("plugin_version", "v1.2.3"), ("running_sha256", "bad"),
                             ("config", []), ("type", None), ("plugin_version", False),
                             ("running_plugin_version", False)):
            case = copy.deepcopy(BEFORE)
            case["existing/jwt/"][field] = value
            cases.append(case)
        for case in cases:
            with self.subTest(case=case), self.assertRaises(ValueError):
                CHECK(case)

    def test_explicit_plugin_names_and_nullable_legacy_version(self):
        for field in ("plugin_name", "options", "config"):
            case = copy.deepcopy(BEFORE)
            case["existing/jwt/"].update(type="plugin", plugin_version=None,
                                           running_plugin_version=None)
            case["existing/jwt/"][field] = PLUGIN if field == "plugin_name" else {"plugin_name": PLUGIN}
            CHECK(case, case, CATALOG)

    def test_wrong_catalog_is_rejected_even_without_new_mounts(self):
        for field, value in (("builtin", True), ("command", PLUGIN),
                             ("version", ""), ("sha256", "bad")):
            with self.subTest(field=field), self.assertRaises(ValueError):
                CHECK(BEFORE, BEFORE, {**CATALOG, field: value})

    def test_command_reads_files_and_rejects_malformed_input_without_disclosure(self):
        with tempfile.TemporaryDirectory() as directory:
            files = [Path(directory) / name for name in ("before.json", "after.json", "target.json")]
            for path, value in zip(files, (BEFORE, BEFORE, CATALOG)):
                path.write_text(json.dumps(value))
            result = subprocess.run([sys.executable, str(SCRIPT), *map(str, files)],
                                    capture_output=True, text=True)
            self.assertEqual(0, result.returncode, result.stderr)
            for bad in ('{"private-fixture":', '[]', '{}'):
                files[0].write_text(bad)
                result = subprocess.run([sys.executable, str(SCRIPT), *map(str, files)],
                                        capture_output=True, text=True)
                self.assertNotEqual(0, result.returncode)
                self.assertNotIn("private-fixture", result.stderr)


if __name__ == "__main__":
    unittest.main()
