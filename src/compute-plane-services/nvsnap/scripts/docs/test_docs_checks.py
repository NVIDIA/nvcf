#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
"""Tests for check-doc-links.py and docs-toc.py: python3 -m unittest, from this directory."""
from __future__ import annotations

import importlib.util
import pathlib
import subprocess
import tempfile
import unittest

HERE = pathlib.Path(__file__).resolve().parent


def load(name: str, file: str):
    spec = importlib.util.spec_from_file_location(name, HERE / file)
    assert spec and spec.loader
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


links = load("check_doc_links", "check-doc-links.py")
toc = load("docs_toc", "docs-toc.py")
ref = load("reference", "reference.py")


class SlugTest(unittest.TestCase):
    # GitHub's anchors, as markdownlint's MD051 also computes them.
    def test_github_slugs(self):
        for heading, want in {
            "What nvsnap is": "what-nvsnap-is",
            "0. Evidence levels": "0-evidence-levels",
            "`nvsnap.io/criu-group` and friends": "nvsnapiocriu-group-and-friends",
            "Server IP is `<pending>` forever": "server-ip-is-pending-forever",
            "Capture / restore": "capture--restore",
            "NVSNAP_DEFAULT_CAPTURE_PATH": "nvsnap_default_capture_path",
            "See [the guide](x.md)": "see-the-guide",
        }.items():
            self.assertEqual(links.slug(heading), want, heading)


class LinkCheckTest(unittest.TestCase):
    def repo(self, files: dict[str, str]) -> pathlib.Path:
        root = pathlib.Path(tempfile.mkdtemp())
        for rel, text in files.items():
            (root / rel).parent.mkdir(parents=True, exist_ok=True)
            (root / rel).write_text(text, encoding="utf-8")
        subprocess.run(["git", "init", "-q", str(root)], check=True)
        subprocess.run(["git", "-C", str(root), "add", "."], check=True)
        return root

    def test_good_links_pass(self):
        root = self.repo({
            "README.md": "# R\n\nSee [arch](docs/A.md#two) and `docs/A.md`.\n",
            "docs/A.md": "# A\n\n## One\n\n## Two\n\n[up](../README.md) [self](#one)\n",
        })
        checked, problems = links.check(root)
        self.assertEqual(problems, [])
        self.assertEqual(checked, 3)

    def test_each_kind_of_breakage_is_reported(self):
        root = self.repo({
            "README.md": "# R\n\n[gone](docs/GONE.md) [bad](docs/A.md#nope) names `docs/OLD.md`.\n",
            "docs/A.md": "# A\n\n[self](#missing)\n",
        })
        _, problems = links.check(root)
        self.assertEqual(sorted(problems), sorted([
            "README.md: missing target docs/GONE.md",
            "README.md: no heading #nope in docs/A.md",
            "README.md: names docs/OLD.md, which does not exist",
            "docs/A.md: no heading #missing in docs/A.md",
        ]))

    def test_doc_paths_named_in_code_must_exist(self):
        root = self.repo({
            "docs/A.md": "# A\n",
            "internal/x.go": "// See docs/A.md and docs/MOVED.md.\npackage x\n",
            "deploy/notes.txt": "record: docs/results/OLD.md\n",
        })
        self.assertEqual(links.check(root)[1], ["internal/x.go: names docs/MOVED.md, which does not exist"])

    def test_fences_and_frozen_docs_are_skipped(self):
        root = self.repo({
            "README.md": "# R\n\n```text\n[x](nowhere.md)\n```\n",
            "docs/results/OLD.md": "# Old\n\n[x](nowhere.md) `docs/NOWHERE.md`\n",
        })
        self.assertEqual(links.check(root)[1], [])


class TocTest(unittest.TestCase):
    DOC = "# Title\n\nIntro.\n\n## First\n\n### Inner\n\n## Second\n\n```text\n## not a heading\n```\n"

    def test_render_places_toc_before_first_section_and_is_stable(self):
        out = toc.render(self.DOC)
        self.assertIn("- [First](#first)\n  - [Inner](#inner)\n- [Second](#second)\n", out)
        self.assertNotIn("not-a-heading", out)
        self.assertLess(out.index(toc.BEGIN), out.index("## First"))
        self.assertGreater(out.index(toc.BEGIN), out.index("Intro."))
        self.assertEqual(toc.render(out), out)

    def test_depth_two_and_short_docs(self):
        self.assertNotIn("[Inner]", toc.render("<!-- toc-depth: 2 -->\n" + self.DOC))
        short = "# T\n\n## Only\n"
        self.assertEqual(toc.render(short), short)
        # A stale TOC in a doc that no longer needs one is removed.
        self.assertEqual(toc.render(toc.render(self.DOC).replace("### Inner\n", "").replace("## Second\n", "")).count(toc.BEGIN), 0)


class ReferenceTest(unittest.TestCase):
    def test_first_sentence_drops_the_name_and_keeps_one_sentence(self):
        self.assertEqual(ref.first_sentence("GPUShareAnnotation opts a pod in. More detail.", "GPUShareAnnotation"),
                         "Opts a pod in.")
        self.assertEqual(ref.first_sentence("Splits a|b at v1.2 safely"), "Splits a\\|b at v1.2 safely")

    def test_declarations_and_routes_parse(self):
        m = ref.DECL.match('\tCRIUGroupAnnotation = "nvsnap.io/criu-group" // trailing')
        self.assertEqual((m.group(1), m.group(2), m.group(3)), ("CRIUGroupAnnotation", "nvsnap.io/criu-group", "trailing"))
        r = ref.ROUTE.search('\trouter.Handle("/debug/pprof/heap", pprof.Handler("heap"))')
        self.assertEqual((r.group(1), r.group(2)), ("/debug/pprof/heap", 'pprof.Handler("heap")'))
        r = ref.ROUTE.search('\trouter.HandleFunc("/v1/x/{id}", a.xHandler).Methods("GET", "POST")')
        self.assertEqual((r.group(2), r.group(3)), ("a.xHandler", '"GET", "POST"'))

    def test_generated_page_is_current(self):
        self.assertEqual(ref.render(), ref.OUT.read_text(encoding="utf-8"),
                         "docs/REFERENCE.md is stale: run scripts/docs/reference.py")


if __name__ == "__main__":
    unittest.main()
