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

# A synthetic planned entry keeps the planned-recipe validation covered while the catalog has no real one.
PLANNED = {"schemaVersion": 1, "recipes": [{
    "id": "example-planned", "name": "Example Planned", "servedModelId": None,
    "availability": {"status": "planned", "deployable": False, "reason": "Needs chart support.",
                     "checkedAt": "2026-10-12", "sources": ["https://example.invalid/model"]},
    "precision": None, "license": None, "licenseNotice": "notices/example-planned/NOTICE",
    "model": None, "runtime": None, "profiles": [],
    "upstreamCandidates": [{
        "id": "example-tp4", "model": {"repository": "example/model", "revision": "a" * 40},
        "license": "MIT", "precision": "FP8",
        "runtime": {"backend": "sglang", "image": "example/runtime@sha256:" + "b" * 64},
        "hardware": {"os": "linux", "architecture": "arm64", "gpuProduct": "EXAMPLE", "modelNodeCount": 1,
                     "gpusPerNode": 4, "tensorParallelSize": 4},
        "validation": {"status": "unqualified", "scope": "Documentation only."}}]}]}


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

    def test_release_names_match_recipe_sources(self):
        expected = {model['id']: model['releaseName'] for model in catalog.read(ROOT/'catalog.json')['models']}
        expected['glm-5.3'] = catalog.read(ROOT/'glm-5.3/recipe.json')['releaseName']
        for identifier, release in expected.items():
            self.assertRegex(release, r'^[a-z0-9][a-z0-9-]*$')
            for profile in self.recipes[identifier]['profiles']:
                self.assertEqual(profile['deployment']['releaseName'], release)

    def test_available_and_planned_notice_paths_share_one_bundle_file(self):
        for identifier, recipe in self.recipes.items():
            with self.subTest(recipe=identifier):
                expected = None if recipe["availability"]["status"] == "unavailable" else "NOTICE"
                self.assertEqual(recipe["licenseNotice"], expected)

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

    def test_gguf_export_rejects_profile_tuning_drift(self):
        original_read = catalog.read
        def changed_read(path):
            result = original_read(path)
            if path == ROOT / 'glm-5.3/recipe.json':
                result['tuning']['unified']['slots'] += 1
            return result
        with patch.object(catalog, 'read', side_effect=changed_read), self.assertRaisesRegex(ValueError, 'must match recipe tuning'):
            catalog.gguf_recipes(ROOT)

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

    def export_flash(self, update):
        original_read = catalog.read
        source = original_read(ROOT / "catalog.json")
        model = next(model for model in source["models"] if model["id"] == "qwen3.8-flash-next")
        for owner in [model, *model["profiles"]]:
            for key in ("validatedWorkload", "automaticHelmValidation"):
                owner.pop(key, None)
        update(model)
        with patch.object(catalog, "read", side_effect=lambda path: source if path == ROOT / "catalog.json" else original_read(path)):
            return next(recipe for recipe in catalog.sglang_recipes(ROOT) if recipe["id"] == model["id"])

    def test_flash_profiles_require_capabilities_and_automatic_guide(self):
        for profile in self.recipes["qwen3.8-flash-next"]["profiles"]:
            with self.subTest(profile=profile["id"]):
                deployment = profile["deployment"]
                self.assertEqual(deployment["lifecycle"], "automatic")
                self.assertEqual(deployment["values"], {"recipe": "qwen3.8-flash-next", "profileName": profile["id"]})
                # Only a profile with NVMe offload or a fabric link needs verified node capabilities.
                needs_facts = profile["fabric"] is not None or "offloadBytes" in profile["perNode"][0]["storage"]
                self.assertEqual("nodeCapabilities" in deployment["requiredSiteValues"], needs_facts)
                self.assertEqual(deployment["guide"], "../ADVANCED.md#helm-flash-next-recipes")

    def test_flash_validation_records_are_independent_per_profile(self):
        def update(model):
            for i, profile in enumerate(model["profiles"]):
                profile["validatedWorkload"] = {"profile": profile["id"], "contextLength": 8192 + i}
                profile["automaticHelmValidation"] = {"profile": profile["id"], "modelRevision": model["revision"],
                    "runtimeImage": model["image"], "status": "smoke-tested" if i == 0 else "failed"}
        recipe = self.export_flash(update)
        for i, profile in enumerate(recipe["profiles"]):
            with self.subTest(profile=profile["id"]):
                validation = profile["validation"]
                self.assertEqual(validation["runtimeStatus"], "smoke-tested")
                self.assertEqual(validation["workload"]["profile"], profile["id"])
                self.assertEqual(validation["workload"]["contextLength"], 8192 + i)
                self.assertEqual(validation["automaticHelmStatus"], "smoke-tested" if i == 0 else "failed")
                self.assertEqual(validation["automaticHelmWorkload"]["profile"], profile["id"])

    def test_model_level_validation_applies_only_to_its_profile(self):
        def update(model):
            profile = model["profiles"][0]
            model["validatedWorkload"] = {"profile": profile["id"]}
            model["automaticHelmValidation"] = {"profile": profile["id"], "modelRevision": model["revision"],
                "runtimeImage": model["image"]}
        recipe = self.export_flash(update)
        for i, profile in enumerate(recipe["profiles"]):
            expected = "smoke-tested" if i == 0 else "pending"
            self.assertEqual(profile["validation"]["runtimeStatus"], expected)
            self.assertEqual(profile["validation"]["automaticHelmStatus"], expected)

    def test_profile_records_override_or_clear_model_level_validation(self):
        def update(model):
            profile = model["profiles"][0]
            model["validatedWorkload"] = {"profile": profile["id"], "contextLength": 4096}
            model["automaticHelmValidation"] = {"profile": profile["id"], "modelRevision": model["revision"],
                "runtimeImage": model["image"]}
            profile["validatedWorkload"] = {"profile": profile["id"], "contextLength": 8192}
            profile["automaticHelmValidation"] = None
        validation = self.export_flash(update)["profiles"][0]["validation"]
        self.assertEqual(validation["workload"]["contextLength"], 8192)
        self.assertEqual(validation["automaticHelmStatus"], "pending")
        self.assertIsNone(validation["automaticHelmWorkload"])

    def test_profile_validation_rejects_wrong_profile_or_stale_pins(self):
        for key in ("validatedWorkload", "automaticHelmValidation"):
            def update(model):
                model["profiles"][0][key] = {"profile": model["profiles"][1]["id"]}
            with self.subTest(key=key), self.assertRaisesRegex(ValueError, "another profile"):
                self.export_flash(update)
        def stale(model):
            model["profiles"][0]["automaticHelmValidation"] = {"profile": model["profiles"][0]["id"],
                "modelRevision": "old", "runtimeImage": model["image"]}
        with self.assertRaisesRegex(ValueError, "pins changed"):
            self.export_flash(stale)

    def test_inventory_keeps_every_executable_recipe_deployable(self):
        available = {"qwen3.8-27b", "qwen3.8-27b-nvfp4", "qwen3.8-flash-next", "glm-5.3", "nemotron-nano-12b-v2",
                     "nemotron-3.5-lightning", "qwen3.5-4b", "deepseek-v4-flash"}
        planned = {recipe["id"] for recipe in catalog.read(ROOT / "planned.json")["recipes"]}
        self.assertEqual(set(self.recipes), available | planned)
        deployable = {recipe["id"] for recipe in self.recipes.values() if recipe["availability"]["deployable"]}
        self.assertEqual(deployable, available)
        for identifier in available:
            self.assertEqual(self.recipes[identifier]["availability"]["status"], "available")
            self.assertTrue(self.recipes[identifier]["profiles"])

    def test_planned_entries_have_no_deployment_or_unverified_artifact_pins(self):
        for source in (catalog.read(ROOT / "planned.json"), PLANNED):
            with patch.object(catalog, "read", return_value=source):
                exported = {recipe["id"]: recipe for recipe in catalog.planned_recipes(ROOT)}
            self.assertEqual(set(exported), {recipe["id"] for recipe in source["recipes"]})
            for recipe in source["recipes"]:
                entry = exported[recipe["id"]]
                self.assertEqual(entry, recipe)
                self.assertIs(entry["availability"]["deployable"], False)
                self.assertEqual(entry["profiles"], [])
                self.assertIsNone(entry["servedModelId"])
                for key in ("model", "runtime", "license", "precision"):
                    self.assertIsNone(entry[key])
                self.assertNotIn("deployment", entry)
        for recipe in catalog.read(ROOT / "planned.json")["recipes"]:
            self.assertEqual(self.recipes[recipe["id"]]["profiles"], [])
            self.assertIsNone(self.recipes[recipe["id"]]["servedModelId"])

    def test_planned_export_rejects_accidentally_executable_metadata(self):
        mutations = [("profiles", [{"deployment": {"chart": "example"}}]), ("servedModelId", "invented"),
                     ("model", {"repository": "invented/model"}), ("deployment", {"chart": "example"}), ("chart", "example")]
        for key, value in mutations:
            with self.subTest(key=key):
                modified = copy.deepcopy(PLANNED)
                modified["recipes"][0][key] = value
                with patch.object(catalog, "read", return_value=modified), self.assertRaisesRegex(ValueError, "must not expose"):
                    catalog.planned_recipes(ROOT)
        modified = copy.deepcopy(PLANNED)
        modified["recipes"][0]["availability"]["deployable"] = True
        with patch.object(catalog, "read", return_value=modified), self.assertRaisesRegex(ValueError, "must not expose"):
            catalog.planned_recipes(ROOT)

    def test_planned_upstream_candidates_are_pinned_research_not_installer_inputs(self):
        sources = [catalog.read(ROOT / "planned.json"), PLANNED]
        candidates = [candidate for source in sources for recipe in source["recipes"] for candidate in recipe["upstreamCandidates"]]
        self.assertTrue(candidates)
        for candidate in candidates:
            self.assertRegex(candidate["model"]["revision"], r"^[0-9a-f]{40}$")
            self.assertRegex(candidate["runtime"]["image"], r"@sha256:[0-9a-f]{64}$")
            self.assertEqual(candidate["validation"]["status"], "unqualified")
            self.assertNotIn("deployment", candidate)
        import model_capacity
        real_read = catalog.read
        for source in sources:
            with patch.object(catalog, "read", side_effect=lambda path, source=source: source if Path(path).name == "planned.json" else real_read(path)):
                built = catalog.build(ROOT)
            for entry in source["recipes"]:
                with self.subTest(recipe=entry["id"]):
                    report = model_capacity.analyze({'nodes': [], 'pods': []}, built, entry['id'])
                    self.assertEqual(report['status'], 'unsupported')
                    self.assertEqual(report['chosenNodes'], [])
        for chart in ("sglang", "gguf-backend"):
            for path in (ROOT / "charts" / chart / "files").rglob("profiles.json"):
                content = path.read_text()
                for candidate in candidates:
                    self.assertNotIn(candidate["model"]["revision"], content)
                    self.assertNotIn(candidate["runtime"]["image"], content)

    def override_runtime(self, **fields):
        real_read = catalog.read

        def read(path):
            data = real_read(path)
            if Path(path).name == "catalog.json":
                data = copy.deepcopy(data)
                data["models"][0]["profiles"][0].update(fields)
            return data
        return patch.object(catalog, "read", side_effect=read)

    def test_a_profile_runtime_override_needs_both_fields_a_digest_and_matching_validation(self):
        digest = "example/runtime@sha256:" + "c" * 64
        for fields, message in (({"image": digest}, "both image and runtime"), ({"runtime": "SGLang"}, "both image and runtime"),
                                ({"image": "example/runtime:latest", "runtime": "SGLang"}, "digest-pinned"),
                                ({"image": digest, "runtime": "SGLang"}, "pins changed")):
            with self.subTest(fields=fields), self.override_runtime(**fields), self.assertRaisesRegex(ValueError, message):
                catalog.build(ROOT)

    def test_deepseek_station_profile_uses_two_exclusive_gpus_and_its_own_pinned_runtime(self):
        recipe = self.recipes["deepseek-v4-flash"]
        self.assertRegex(recipe["model"]["revision"], r"^[0-9a-f]{40}$")
        self.assertRegex(recipe["runtime"]["image"], r"@sha256:[0-9a-f]{64}$")
        profiles = {profile["id"]: profile for profile in recipe["profiles"]}
        self.assertEqual(set(profiles), {"spark-tp2", "station-fp4-tp2"})
        station = profiles["station-fp4-tp2"]
        self.assertEqual(station["modelNodeCount"], 2)
        self.assertEqual(len(station["perNode"]), 2)
        self.assertEqual(station["hardware"]["memoryMode"], "discrete")
        self.assertRegex(station["runtime"]["image"], r"@sha256:[0-9a-f]{64}$")
        self.assertNotEqual(station["runtime"]["image"], recipe["runtime"]["image"])
        self.assertNotIn("runtime", profiles["spark-tp2"])
        self.assertEqual(recipe["model"]["repository"], "deepseek-ai/DeepSeek-V4-Flash")

    def test_upstream_candidates_reject_deployment_and_mutable_references(self):
        for field, value, message in (("chart", "example", "installable deployment"),
                                      ("model", {"revision": "main"}, "immutable"),
                                      ("runtime", {"image": "image:latest"}, "immutable")):
            modified = copy.deepcopy(PLANNED)
            modified["recipes"][0]["upstreamCandidates"][0][field] = value
            with self.subTest(field=field), patch.object(catalog, "read", return_value=modified), \
                    self.assertRaisesRegex(ValueError, message):
                catalog.planned_recipes(ROOT)

    def test_availability_evidence_is_dated_and_cites_sources(self):
        for source in (catalog.read(ROOT / "planned.json"), PLANNED):
            for recipe in source["recipes"]:
                evidence = recipe["availability"]
                self.assertTrue(evidence["sources"])
                self.assertRegex(evidence["checkedAt"], r"^\d{4}-\d{2}-\d{2}$")
                self.assertTrue(evidence["reason"])

    def committed_descriptions(self):
        sources = {model["id"]: model.get("description") for model in catalog.read(ROOT / "catalog.json")["models"]}
        for path in sorted(ROOT.glob("*/profiles.json")):
            metadata = catalog.read(path)
            sources[metadata["recipe"]] = catalog.read(path.parent / metadata["recipeFile"]).get("description")
        return sources

    def test_every_recipe_has_a_valid_description_that_the_index_exports_unchanged(self):
        sources = self.committed_descriptions()
        self.assertEqual(set(sources), {recipe["id"] for recipe in self.index["recipes"]})
        for identifier, description in sources.items():
            with self.subTest(recipe=identifier):
                self.assertTrue(description, "Every catalog recipe needs a description for the demo UI card")
                self.assertEqual(self.recipes[identifier]["description"], description)
                self.assertEqual(catalog.checked_description(description, identifier), description)

    def test_recipes_that_share_a_card_share_one_description(self):
        # The UI groups recipes by name and shows the first description it finds.
        by_name = {}
        for recipe in self.index["recipes"]:
            by_name.setdefault(recipe["name"], set()).add(recipe["description"])
        for name, descriptions in by_name.items():
            with self.subTest(card=name):
                self.assertEqual(len(descriptions), 1)

    def test_description_validation_applies_to_both_backends(self):
        real_read = catalog.read

        def with_description(value, backend):
            def read(path):
                data = real_read(path)
                path = Path(path)
                if backend == "sglang" and path.name == "catalog.json":
                    data = copy.deepcopy(data)
                    data["models"][0]["description"] = value
                if backend == "gguf" and path.name == "recipe.json" and path.parent.name == "glm-5.3":
                    data = copy.deepcopy(data)
                    data["description"] = value
                return data
            return read
        for backend in ("sglang", "gguf"):
            with patch.object(catalog, "read", side_effect=with_description("A short plain description.", backend)):
                built = {item["id"]: item for item in catalog.build(ROOT)["recipes"]}
            changed = real_read(ROOT / "catalog.json")["models"][0]["id"] if backend == "sglang" else "glm-5.3"
            self.assertEqual(built[changed]["description"], "A short plain description.")
            for bad in ("", "   ", 7, ["list"], "x" * 601, "two\nlines", "caf\u00e9"):
                with self.subTest(backend=backend, description=bad), patch.object(catalog, "read", side_effect=with_description(bad, backend)), \
                        self.assertRaisesRegex(ValueError, "short single-line ASCII"):
                    catalog.build(ROOT)

    def test_deepseek_evidence_uses_publisher_checkpoint_and_primary_tp2_example(self):
        source = next(m for m in catalog.read(ROOT / "catalog.json")["models"] if m["id"] == "deepseek-v4-flash")
        evidence = source["artifactEvidence"]
        self.assertIn("https://github.com/sgl-project/sglang/blob/main/docs/demo/deepseek_v4_flash.ipynb", evidence["sources"])
        self.assertRegex(evidence["checkedAt"], r"^\d{4}-\d{2}-\d{2}$")
        self.assertGreater(evidence["weightBytes"], 0)

    def test_chart_references_resolve_to_local_packages(self):
        for recipe in self.recipes.values():
            for profile in recipe["profiles"]:
                chart = profile["deployment"]["chart"]
                self.assertTrue((ROOT / chart["localPath"] / "Chart.yaml").is_file())
                self.assertEqual(chart["archive"], f"{chart['name']}-{chart['version']}.tgz")
                self.assertEqual(chart["publication"], "local")
                self.assertIsNone(chart["repository"])

    def test_optional_oci_repositories_are_independent_per_recipe_and_backend(self):
        original_read = catalog.read
        expected = {
            'qwen3.8-27b': 'oci://registry.example.com/qwen/pylon-sglang-recipe',
            'qwen3.8-flash-next': 'oci://other.example.com:5443/team/flash/pylon-sglang-recipe',
            'glm-5.3': 'oci://registry.example.com/gguf/pylon-gguf-backend',
        }
        def changed_read(path):
            value = original_read(path)
            if path == ROOT / 'catalog.json':
                for model in value['models']:
                    model['chartRepository'] = expected.get(model['id'])
            elif path == ROOT / 'glm-5.3/recipe.json':
                value['chartRepository'] = expected['glm-5.3']
            return value
        with patch.object(catalog, 'read', side_effect=changed_read):
            recipes = catalog.build()['recipes']
        for recipe in recipes:
            for profile in recipe['profiles']:
                chart = profile['deployment']['chart']
                baseline = next(p for p in self.recipes[recipe['id']]['profiles'] if p['id'] == profile['id'])['deployment']['chart']
                self.assertEqual(chart['repository'], expected.get(recipe['id']))
                self.assertEqual(chart['publication'], 'oci' if recipe['id'] in expected else 'local')
                self.assertEqual(chart['localPath'], baseline['localPath'])
                self.assertEqual(chart['archive'], baseline['archive'])
        self.assertTrue(all(p['deployment']['chart']['repository'] is None
                            for recipe in catalog.build()['recipes'] for p in recipe['profiles']))

    def test_chart_repositories_reject_malformed_or_incomplete_references(self):
        good = 'oci://registry.example.com/charts/pylon-sglang-recipe'
        invalid = (False, 1, [], {}, '', 'registry.example.com/charts/pylon-sglang-recipe',
                   good.replace('oci:', 'https:'), 'oci:///pylon-sglang-recipe',
                   'oci://registry.example.com', 'oci://registry.example.com/charts',
                   good + ':0.2.0', good + '@sha256:' + 'a' * 64, good + '?tag=0.2.0',
                   good + '?', good + '#part', good + '#', good + '/', good + ' ',
                   good.replace('registry.', 'user:password@registry.'),
                   good.replace('charts/', '../'), good.replace('charts/', 'bad%2Fpath/'),
                   good.replace('charts/', 'charts//'), good.replace('charts/', 'Charts/'),
                   good.replace('registry.', 'bad_host.'), good.replace('.com/', '.com:0/'),
                   good.replace('.com/', '.com:65536/'), '\n' + good, good + '\x00')
        for repository in invalid:
            with self.subTest(repository=repository), self.assertRaises(ValueError):
                catalog.chart_reference(ROOT, Path('charts/sglang'), repository)

    def test_source_guides_resolve_relative_to_index(self):
        for recipe in self.recipes.values():
            for profile in recipe["profiles"]:
                guide = profile["deployment"]["guide"].split("#", 1)[0]
                self.assertTrue((ROOT / guide).is_file(), guide)

    def test_automatic_support_tracks_bundled_recipes(self):
        supported = set(catalog.read(ROOT / "charts/sglang/files/profiles.json"))
        for recipe in self.recipes.values():
            if (recipe["runtime"] or {}).get("backend") != "sglang":
                continue
            for profile in recipe["profiles"]:
                self.assertIn(recipe["id"], supported)
                self.assertEqual(profile["deployment"]["lifecycle"], "automatic")

    def test_validation_records_preserve_scope_and_artifact_pins(self):
        for recipe in self.recipes.values():
            for profile in recipe["profiles"]:
                validation = profile["validation"]
                self.assertIn(validation["automaticHelmStatus"], ("pending", "smoke-tested", "failed"))
                if validation["automaticHelmStatus"] == "smoke-tested":
                    record = validation["automaticHelmWorkload"]
                    self.assertEqual(record["modelRevision"], recipe["model"]["revision"])
                    self.assertEqual(record["runtimeImage"], profile.get("runtime", recipe["runtime"])["image"])
                    self.assertEqual(record["profile"], profile["id"])
                if validation["workload"]:
                    if validation["workload"].get("contextBoundaryExercised"):
                        record = validation["automaticHelmWorkload"]
                        self.assertIn("context-boundary", record["checks"])
                        self.assertEqual(record["observedWorkload"]["boundaryHttpStatus"], 200)
                        self.assertEqual(record["observedWorkload"]["boundaryPromptTokens"] +
                                         record["observedWorkload"]["boundaryMaxOutputTokens"],
                                         record["contextLength"])
                    if validation["workload"]["fullContextExercised"]:
                        self.assertGreaterEqual(validation["workload"]["maximumObservedTotalTokens"],
                                                validation["workload"]["contextLength"])
                    self.assertFalse(validation["workload"]["performanceBenchmarked"])
                    self.assertFalse(validation["workload"]["qualityEvaluated"])
                else:
                    self.assertEqual(validation["runtimeStatus"], "pending")


if __name__ == "__main__":
    unittest.main()
