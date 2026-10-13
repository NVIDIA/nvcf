# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Packaged model charts carry the model terms of every recipe they bundle."""
import importlib.util
import json
from pathlib import Path, PurePosixPath
import re
import shutil
import subprocess
import tarfile
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
SPEC = importlib.util.spec_from_file_location("export_catalog_notices", ROOT / "export_catalog.py")
catalog = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(catalog)

CHARTS = (("charts/sglang", "pylon-sglang-recipe", "sglang"),
          ("charts/gguf-backend", "pylon-gguf-backend", "llama.cpp"))


def package(chart_dir):
    """Archive member contents by name, as helm package writes them."""
    with tempfile.TemporaryDirectory() as directory:
        subprocess.run(["helm", "package", str(chart_dir), "--destination", directory],
                       check=True, capture_output=True, text=True)
        with tarfile.open(next(Path(directory).glob("*.tgz"))) as archive:
            return {member.name: archive.extractfile(member).read()
                    for member in archive.getmembers() if member.isfile()}


def bundled_recipes(members, chart):
    """SGLang charts bundle a profile map keyed by recipe; GGUF charts bundle one folder per recipe."""
    if chart == "pylon-sglang-recipe":
        return set(json.loads(members[f"{chart}/files/profiles.json"]))
    prefix = f"{chart}/files/recipes/"
    return {PurePosixPath(name[len(prefix):]).parts[0] for name in members if name.startswith(prefix)}


def recipes_missing_terms(members, chart, backend):
    """Bundled recipes whose listing or maintained terms are absent from the packaged NOTICE."""
    notice = members.get(f"{chart}/NOTICE", b"").decode()
    missing = []
    for recipe in sorted(bundled_recipes(members, chart)):
        terms = catalog.recipe_notice(ROOT, {"id": recipe, "runtime": {"backend": backend}}).read_text().strip()
        if not re.search(rf"(?m)^- {re.escape(recipe)}: \S", notice) or terms not in notice:
            missing.append(recipe)
    return missing


@unittest.skipUnless(shutil.which("helm"), "Helm is required")
class PackagedChartNoticeTests(unittest.TestCase):
    def test_packaged_model_charts_carry_terms_for_every_bundled_recipe(self):
        for chart_dir, chart, backend in CHARTS:
            with self.subTest(chart=chart):
                members = package(ROOT / chart_dir)
                self.assertTrue(f"{chart}/NOTICE" in members, f"{chart} archive has no NOTICE")
                self.assertTrue(bundled_recipes(members, chart))
                self.assertEqual(recipes_missing_terms(members, chart, backend), [])

    def test_check_reports_bundled_recipes_without_terms(self):
        with tempfile.TemporaryDirectory() as directory:
            chart_dir = Path(directory) / "sglang"
            shutil.copytree(ROOT / "charts/sglang", chart_dir)
            notice = chart_dir / "NOTICE"
            notice.write_text(re.sub(r"(?m)^- qwen3\.5-4b: .*\n", "", notice.read_text()))
            members = package(chart_dir)
            self.assertEqual(recipes_missing_terms(members, "pylon-sglang-recipe", "sglang"), ["qwen3.5-4b"])
            notice.unlink()
            members = package(chart_dir)
            self.assertEqual(recipes_missing_terms(members, "pylon-sglang-recipe", "sglang"),
                             sorted(bundled_recipes(members, "pylon-sglang-recipe")))


if __name__ == "__main__":
    unittest.main()
