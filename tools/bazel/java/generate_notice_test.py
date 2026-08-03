import unittest
from unittest import mock

import generate_notice


class NoticeMetadataTest(unittest.TestCase):
    def test_merge_rejects_component_duplicate(self):
        entry = {"licenses": ["MIT"], "name": "Example", "url": ""}
        shared = [("shared.json", {"artifacts": {"g:a:1": entry}})]

        with self.assertRaisesRegex(ValueError, "duplicates shared metadata"):
            generate_notice.merge_metadata(
                shared,
                {"artifacts": {"g:a:1": entry}},
            )

    def test_prune_removes_exact_shared_entries(self):
        shared_entry = {"licenses": ["MIT"], "name": "Shared", "url": ""}
        local_entry = {"licenses": ["Apache-2.0"], "name": "Local", "url": ""}
        result = generate_notice.prune_shared_metadata(
            {"artifacts": {"g:a:1": shared_entry, "g:b:2": local_entry}},
            [("shared.json", {"artifacts": {"g:a:1": shared_entry}})],
        )

        self.assertEqual({"g:b:2": local_entry}, result["artifacts"])

    def test_inventory_normalizes_unambiguous_license_aliases(self):
        inventory = generate_notice.generated_inventory(
            ["g:a"],
            {"artifacts": {"g:a": {"version": "1"}}},
            {
                "artifacts": {
                    "g:a:1": {
                        "licenses": ["Apache License, Version 2.0"],
                        "name": "Example",
                        "url": "https://example.invalid",
                    }
                }
            },
            {"Apache License, Version 2.0": "Apache-2.0"},
        )

        self.assertEqual(
            ["Apache-2.0"],
            inventory["dependencies"][0]["licenses"],
        )

    def test_designated_license_controls_notice_and_inventory(self):
        metadata = {
            "artifacts": {
                "g:a:1": {
                    "licenses": ["Apache License, Version 2.0", "MIT License"],
                    "designated_license": "Apache-2.0",
                    "name": "Example",
                    "url": "https://example.invalid",
                }
            }
        }
        maven_install = {"artifacts": {"g:a": {"version": "1"}}}
        aliases = {
            "Apache License, Version 2.0": "Apache-2.0",
            "MIT License": "MIT",
        }

        notice = generate_notice.generated_notice(
            ["g:a"], maven_install, metadata, aliases
        )
        inventory = generate_notice.generated_inventory(
            ["g:a"], maven_install, metadata, aliases
        )

        self.assertIn(
            "(Designated: Apache-2.0; upstream: Apache-2.0 OR MIT) Example",
            notice,
        )
        self.assertEqual(
            {
                "coordinate": "g:a:1",
                "licenses": ["Apache-2.0"],
                "declared_licenses": ["Apache-2.0", "MIT"],
                "designated_license": "Apache-2.0",
                "name": "Example",
                "url": "https://example.invalid",
            },
            inventory["dependencies"][0],
        )

    def test_designated_license_must_match_upstream_license(self):
        entry = {
            "licenses": ["MIT License"],
            "designated_license": "Apache-2.0",
        }

        with self.assertRaisesRegex(ValueError, "not one of the upstream licenses"):
            generate_notice.designated_license(entry, {"MIT License": "MIT"})

    def test_update_metadata_preserves_designated_license(self):
        maven_install = {
            "artifacts": {"g:a": {"version": "1"}},
            "dependencies": {},
            "repositories": [],
        }
        existing = {
            "artifacts": {
                "g:a:1": {
                    "licenses": ["Apache License, Version 2.0"],
                    "designated_license": "Apache-2.0",
                    "name": "Old name",
                    "url": "",
                }
            }
        }

        resolver = generate_notice.PomMetadataResolver(maven_install)
        resolver.resolve = lambda group_id, artifact_id, version: {
            "licenses": ["Apache License, Version 2.0", "MIT License"],
            "name": "Updated name",
            "url": "https://example.invalid",
        }

        with mock.patch.object(
            generate_notice, "PomMetadataResolver", return_value=resolver
        ):
            result = generate_notice.update_metadata(
                ["g:a"], maven_install, existing
            )

        self.assertEqual(
            "Apache-2.0",
            result["artifacts"]["g:a:1"]["designated_license"],
        )

    def test_delta_uses_exact_versioned_coordinates(self):
        current = {
            "dependencies": [
                {
                    "coordinate": "g:a:2",
                    "licenses": ["MIT"],
                    "name": "A",
                    "url": "",
                },
                {
                    "coordinate": "g:b:1",
                    "licenses": ["Apache-2.0"],
                    "name": "B",
                    "url": "",
                },
            ]
        }
        baseline = {
            "dependencies": [
                {
                    "coordinate": "g:a:1",
                    "licenses": ["MIT"],
                    "name": "A",
                    "url": "",
                }
            ]
        }

        delta = generate_notice.generated_delta(current, [baseline])

        self.assertEqual(
            ["g:a:2", "g:b:1"],
            [entry["coordinate"] for entry in delta["dependencies"]],
        )
        self.assertIn("## MIT", generate_notice.generated_delta_markdown(delta))


if __name__ == "__main__":
    unittest.main()
