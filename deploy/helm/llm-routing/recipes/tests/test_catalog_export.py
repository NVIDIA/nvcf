# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
import copy
import importlib.util
import json
from pathlib import Path
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("export_catalog", ROOT / "export_catalog.py")
catalog = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(catalog)


class CommonCatalogTests(unittest.TestCase):
    def setUp(self):
        self.index = catalog.build()
        self.recipes = {recipe["id"]: recipe for recipe in self.index["recipes"]}

    def test_committed_index_is_current(self):
        self.assertEqual(json.loads((ROOT / "index.json").read_text()), self.index)

    def test_every_source_profile_and_pin_is_exported(self):
        for source in catalog.read(ROOT / "catalog.json")["models"]:
            recipe = self.recipes[source["id"]]
            self.assertEqual(recipe["model"]["revision"], source["revision"])
            self.assertEqual(recipe["runtime"]["image"], source["image"])
            self.assertEqual({p["id"] for p in recipe["profiles"]}, {p["id"] for p in source["profiles"]})
            for source_profile, profile in zip(source["profiles"], recipe["profiles"]):
                self.assertEqual(profile["modelNodeCount"], len(profile["perNode"]))
                self.assertEqual(profile["perNode"][0]["resources"]["memoryRequestBytes"], source_profile["memoryGiB"] * 2**30)
                self.assertEqual(profile["perNode"][0]["storage"]["claimRequestBytes"], source_profile["cacheGiB"] * 2**30)
                self.assertEqual(profile["hardware"]["memoryMode"], source_profile["hardware"]["memoryMode"])

    def test_gguf_storage_and_resources_stay_per_rank(self):
        source = catalog.read(ROOT / "glm-5.3/profiles.json")["profiles"][0]
        recipe = self.recipes["glm-5.3"]
        profile = recipe["profiles"][0]
        self.assertEqual(recipe["servedModelId"], catalog.read(ROOT / "glm-5.3/recipe.json")["servedName"])
        self.assertEqual(recipe["model"]["weightBytes"], catalog.read(ROOT / "glm-5.3/model.lock.json")["weightFileBytes"])
        self.assertEqual(profile["modelNodeCount"], source["nodes"])
        for actual, role in zip(profile["perNode"], source["roles"]):
            self.assertEqual(actual["storage"]["claimRequestBytes"], role["storageGiB"] * 2**30)
            self.assertEqual(actual["servingResources"]["cpuRequestMillicores"], int(role["resources"]["requests"]["cpu"]) * 1000)

        self.assertEqual(profile["perNode"][0]["resources"]["cpuRequestMillicores"], 8000)
        self.assertEqual(profile["perNode"][0]["additionalWorkloads"][0]["resources"]["memoryRequestBytes"], 128 * 2**20)

    def test_changed_pins_invalidate_automatic_helm_validation(self):
        with self.assertRaisesRegex(ValueError, "pins changed"):
            catalog.helm_validation({"profile": "tested", "modelRevision": "old", "runtimeImage": "pinned"},
                                    "tested", "new", "pinned")

    def test_failed_live_validation_is_exported_as_failed(self):
        record = {"profile": "tested", "modelRevision": "pinned-model", "runtimeImage": "pinned-image",
                  "status": "failed", "reason": "host_available"}
        result = catalog.helm_validation(record, "tested", "pinned-model", "pinned-image")
        self.assertEqual(result["automaticHelmStatus"], "failed")
        self.assertEqual(result["automaticHelmWorkload"], record)

    def test_inventory_covers_seven_families_with_two_qwen_precisions(self):
        self.assertEqual(set(self.recipes), {"qwen3.8-27b", "qwen3.8-27b-nvfp4", "qwen3.8-flash-next",
            "glm-5.3", "nemotron-5-nano-12b", "nemotron-5-super-49b", "qwen3.8-4b", "deepseek-v4-flash"})
        self.assertEqual(len({recipe["name"] for recipe in self.recipes.values()}), 7)
        available = {recipe["id"] for recipe in self.recipes.values() if recipe["availability"]["deployable"]}
        self.assertEqual(available, {"qwen3.8-27b", "qwen3.8-27b-nvfp4", "qwen3.8-flash-next", "glm-5.3"})
        for identifier in available:
            self.assertEqual(self.recipes[identifier]["availability"]["status"], "available")
            self.assertTrue(self.recipes[identifier]["profiles"])

    def test_unavailable_entries_have_no_deployment_or_unverified_artifact_pins(self):
        source = catalog.read(ROOT / "planned.json")["recipes"]
        self.assertEqual(len(source), 4)
        for recipe in source:
            exported = self.recipes[recipe["id"]]
            self.assertEqual(exported, recipe)
            self.assertIs(exported["availability"]["deployable"], False)
            self.assertEqual(exported["profiles"], [])
            self.assertIsNone(exported["servedModelId"])
            for key in ("model", "runtime", "license", "precision"):
                self.assertIsNone(exported[key])
            self.assertNotIn("deployment", exported)
            if exported["availability"]["status"] == "unavailable":
                self.assertIsNone(exported["licenseNotice"])
                self.assertEqual(exported["upstreamCandidates"], [])

    def test_planned_export_rejects_accidentally_executable_metadata(self):
        original = catalog.read(ROOT / "planned.json")
        mutations = [("profiles", [{"deployment": {"chart": "example"}}]), ("servedModelId", "invented"),
                     ("model", {"repository": "invented/model"}), ("deployment", {"chart": "example"}), ("chart", "example")]
        for key, value in mutations:
            with self.subTest(key=key):
                modified = copy.deepcopy(original)
                modified["recipes"][0][key] = value
                with patch.object(catalog, "read", return_value=modified), self.assertRaisesRegex(ValueError, "must not expose"):
                    catalog.planned_recipes(ROOT)
        modified = copy.deepcopy(original)
        modified["recipes"][0]["availability"]["deployable"] = True
        with patch.object(catalog, "read", return_value=modified), self.assertRaisesRegex(ValueError, "must not expose"):
            catalog.planned_recipes(ROOT)

    def test_deepseek_candidates_are_pinned_research_not_installer_inputs(self):
        recipe = self.recipes["deepseek-v4-flash"]
        candidates = recipe["upstreamCandidates"]
        self.assertEqual({candidate["hardware"]["architecture"] for candidate in candidates}, {"amd64", "arm64"})
        for candidate in candidates:
            self.assertRegex(candidate["model"]["revision"], r"^[0-9a-f]{40}$")
            self.assertRegex(candidate["runtime"]["image"], r"@sha256:[0-9a-f]{64}$")
            self.assertGreater(candidate["model"]["weightBytes"], 0)
            self.assertEqual(candidate["license"], "MIT")
            self.assertEqual(candidate["hardware"]["gpusPerNode"], 4)
            self.assertEqual(candidate["hardware"]["tensorParallelSize"], 4)
            self.assertEqual(candidate["validation"]["status"], "unqualified")
            self.assertNotIn("deployment", candidate)
        spec = importlib.util.spec_from_file_location("inventory_test_planner", ROOT / "recipes.py")
        planner = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(planner)
        for entry in catalog.read(ROOT / "planned.json")["recipes"]:
            with self.subTest(recipe=entry["id"]), self.assertRaisesRegex(ValueError, "Unknown model"):
                planner.plan({}, {}, [entry["id"]], "test", "storage", "runtime")
        for chart in ("sglang", "gguf-backend"):
            for path in (ROOT / "charts" / chart / "files").rglob("profiles.json"):
                content = path.read_text()
                for candidate in candidates:
                    self.assertNotIn(candidate["model"]["revision"], content)
                    self.assertNotIn(candidate["runtime"]["image"], content)

    def test_upstream_candidates_reject_deployment_and_mutable_references(self):
        for field, value, message in (("chart", "example", "installable deployment"),
                                      ("model", {"revision": "main"}, "immutable"),
                                      ("runtime", {"image": "image:latest"}, "immutable")):
            modified = catalog.read(ROOT / "planned.json")
            candidate = next(recipe for recipe in modified["recipes"] if recipe["upstreamCandidates"])["upstreamCandidates"][0]
            candidate[field] = value
            with self.subTest(field=field), patch.object(catalog, "read", return_value=modified), \
                    self.assertRaisesRegex(ValueError, message):
                catalog.planned_recipes(ROOT)

    def test_availability_evidence_uses_exact_primary_research_sources(self):
        expected = {
            "nemotron-5-nano-12b": "https://huggingface.co/api/models?author=nvidia&search=Nemotron&limit=1000",
            "nemotron-5-super-49b": "https://www.nvidia.com/en-us/ai-data-science/foundation-models/nemotron/",
            "qwen3.8-4b": "https://huggingface.co/api/models/Qwen/Qwen3.8-4B",
            "deepseek-v4-flash": "https://huggingface.co/api/models/deepseek-ai/DeepSeek-V4-Flash",
        }
        for identifier, source in expected.items():
            evidence = self.recipes[identifier]["availability"]
            self.assertIn(source, evidence["sources"])
            self.assertRegex(evidence["checkedAt"], r"^\d{4}-\d{2}-\d{2}$")
            self.assertTrue(evidence["reason"])
        self.assertIn("HTTP 401 is ambiguous", self.recipes["qwen3.8-4b"]["availability"]["reason"])

    def test_chart_references_resolve_to_local_packages(self):
        for recipe in self.recipes.values():
            for profile in recipe["profiles"]:
                chart = profile["deployment"]["chart"]
                self.assertTrue((ROOT / chart["localPath"] / "Chart.yaml").is_file())
                self.assertEqual(chart["archive"], f"{chart['name']}-{chart['version']}.tgz")
                self.assertEqual(chart["publication"], "local")
                self.assertNotIn("oci", chart)

    def test_automatic_support_tracks_bundled_recipes(self):
        supported = set(catalog.read(ROOT / "charts/sglang/files/profiles.json"))
        for recipe in self.recipes.values():
            if (recipe["runtime"] or {}).get("backend") != "sglang":
                continue
            for profile in recipe["profiles"]:
                self.assertEqual(profile["deployment"]["lifecycle"] == "automatic", recipe["id"] in supported)
                if recipe["id"] not in supported:
                    self.assertIsNone(profile["deployment"]["values"])

    def test_runtime_smoke_results_do_not_claim_new_helm_validation(self):
        for recipe in self.recipes.values():
            for profile in recipe["profiles"]:
                validation = profile["validation"]
                self.assertIn(validation["automaticHelmStatus"], ("pending", "unavailable", "smoke-tested", "failed"))
                if validation["automaticHelmStatus"] == "smoke-tested":
                    record = validation["automaticHelmWorkload"]
                    self.assertEqual(record["modelRevision"], recipe["model"]["revision"])
                    self.assertEqual(record["runtimeImage"], recipe["runtime"]["image"])
                    self.assertEqual(record["profile"], profile["id"])
                if validation["workload"]:
                    self.assertFalse(validation["workload"]["fullContextExercised"])
                    self.assertFalse(validation["workload"]["performanceBenchmarked"])
                    self.assertFalse(validation["workload"]["qualityEvaluated"])
                else:
                    self.assertEqual(validation["runtimeStatus"], "pending")


if __name__ == "__main__":
    unittest.main()
