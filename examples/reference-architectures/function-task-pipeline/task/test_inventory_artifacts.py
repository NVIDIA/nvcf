# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import base64
import datetime
import hashlib
import json
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import inventory_artifacts


class InventoryArtifactsTest(unittest.TestCase):
    def test_run_writes_deterministic_report_and_progress(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            models_dir = root / "models"
            resources_dir = root / "resources"
            results_dir = root / "results"
            models_dir.mkdir()
            resources_dir.mkdir()
            (models_dir / "z-layer").mkdir()
            (models_dir / "a-layer").mkdir()
            (models_dir / "z-layer" / "weights.bin").write_bytes(b"model-z")
            (models_dir / "a-layer" / "weights.bin").write_bytes(b"model-a")
            (models_dir / ".nvcf_manifest.json").write_text(
                '{"workerInit":true}\n',
                encoding="utf-8",
            )
            (models_dir / "z-layer" / ".nvcf_manifest.json").write_text(
                '{"artifact":true}\n',
                encoding="utf-8",
            )
            (resources_dir / "z-eval.jsonl").write_text(
                '{"prompt":"hello"}\n',
                encoding="utf-8",
            )
            (resources_dir / "a-eval.jsonl").write_text(
                '{"prompt":"goodbye"}\n',
                encoding="utf-8",
            )

            config = inventory_artifacts.Config(
                models_dir=models_dir,
                resources_dir=resources_dir,
                results_dir=results_dir,
                progress_file=results_dir / "progress",
                task_id="task-test",
                results_location="test-org/test-model",
                workflow_request={"workflowId": "workflow-test"},
            )
            report_path, metadata = inventory_artifacts.run(config)
            first_report_bytes = report_path.read_bytes()
            second_report_path, second_metadata = inventory_artifacts.run(config)

            report = json.loads(first_report_bytes)
            progress = json.loads(config.progress_file.read_text(encoding="utf-8"))
            self.assertEqual(report["workflowRequest"], config.workflow_request)
            self.assertEqual(
                [artifact["path"] for artifact in report["modelArtifacts"]],
                [
                    "a-layer/weights.bin",
                    "z-layer/.nvcf_manifest.json",
                    "z-layer/weights.bin",
                ],
            )
            self.assertEqual(
                [artifact["path"] for artifact in report["datasetArtifacts"]],
                ["a-eval.jsonl", "z-eval.jsonl"],
            )
            self.assertEqual(
                report["modelArtifacts"][0]["sha256"],
                hashlib.sha256(b"model-a").hexdigest(),
            )
            self.assertEqual(
                report["datasetArtifacts"][0]["sha256"],
                hashlib.sha256(b'{"prompt":"goodbye"}\n').hexdigest(),
            )
            self.assertEqual(report["summary"]["modelFileCount"], 3)
            self.assertEqual(report["summary"]["datasetFileCount"], 2)
            self.assertEqual(
                report["summary"]["totalBytes"],
                sum(
                    len(content)
                    for content in (
                        b"model-a",
                        b'{"artifact":true}\n',
                        b"model-z",
                        b'{"prompt":"goodbye"}\n',
                        b'{"prompt":"hello"}\n',
                    )
                ),
            )
            self.assertEqual(second_report_path.read_bytes(), first_report_bytes)
            self.assertEqual(second_metadata, metadata)
            self.assertEqual(
                metadata,
                {
                    "status": "complete",
                    "workflowId": "workflow-test",
                    "resultsLocation": "test-org/test-model",
                    "reportPath": "report.json",
                    **report["summary"],
                    "reportSha256": hashlib.sha256(first_report_bytes).hexdigest(),
                },
            )
            self.assertEqual(progress["taskId"], "task-test")
            self.assertEqual(progress["percentComplete"], 100)
            self.assertEqual(progress["name"], "artifact-inventory")
            self.assertRegex(
                progress["lastUpdatedAt"],
                r"^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{6}Z$",
            )
            parsed_progress_time = datetime.datetime.fromisoformat(
                progress["lastUpdatedAt"].replace("Z", "+00:00")
            )
            self.assertEqual(parsed_progress_time.tzinfo, datetime.timezone.utc)
            self.assertEqual(progress["metadata"], metadata)

    def test_build_report_separates_artifacts_on_shared_volume(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            shared_root = Path(temporary_directory)
            models_dir = shared_root / "model"
            resources_dir = shared_root / "dataset"
            models_dir.mkdir()
            resources_dir.mkdir()
            (models_dir / "weights.bin").write_bytes(b"model")
            (resources_dir / "eval.jsonl").write_bytes(b"dataset")

            config = inventory_artifacts.Config(
                models_dir=models_dir,
                resources_dir=resources_dir,
                results_dir=shared_root / "results",
                progress_file=shared_root / "results" / "progress",
                task_id="task-test",
                results_location="test-org/test-model",
                workflow_request={"workflowId": "workflow-test"},
            )
            report = inventory_artifacts.build_report(config)

            self.assertEqual(
                [artifact["path"] for artifact in report["modelArtifacts"]],
                ["weights.bin"],
            )
            self.assertEqual(
                [artifact["path"] for artifact in report["datasetArtifacts"]],
                ["eval.jsonl"],
            )

    def test_build_report_rejects_missing_dataset_files(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            models_dir = root / "models"
            resources_dir = root / "resources"
            models_dir.mkdir()
            resources_dir.mkdir()
            (models_dir / "weights.bin").write_bytes(b"model")

            config = inventory_artifacts.Config(
                models_dir=models_dir,
                resources_dir=resources_dir,
                results_dir=root / "results",
                progress_file=root / "results" / "progress",
                task_id="task-test",
                results_location="test-org/test-model",
                workflow_request={"workflowId": "workflow-test"},
            )
            with self.assertRaisesRegex(ValueError, "no dataset files found"):
                inventory_artifacts.build_report(config)

    def test_build_report_rejects_missing_model_files(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            models_dir = root / "models"
            resources_dir = root / "resources"
            models_dir.mkdir()
            resources_dir.mkdir()
            (resources_dir / "eval.jsonl").write_text(
                '{"prompt":"hello"}\n',
                encoding="utf-8",
            )

            config = inventory_artifacts.Config(
                models_dir=models_dir,
                resources_dir=resources_dir,
                results_dir=root / "results",
                progress_file=root / "results" / "progress",
                task_id="task-test",
                results_location="test-org/test-model",
                workflow_request={"workflowId": "workflow-test"},
            )
            with self.assertRaisesRegex(ValueError, "no model files found"):
                inventory_artifacts.build_report(config)

    def test_build_report_rejects_file_symlinks(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            models_dir = root / "models"
            resources_dir = root / "resources"
            models_dir.mkdir()
            resources_dir.mkdir()
            (models_dir / "weights.bin").write_bytes(b"model")
            (resources_dir / "eval.jsonl").write_text(
                '{"prompt":"hello"}\n',
                encoding="utf-8",
            )
            outside_file = root / "outside.bin"
            outside_file.write_bytes(b"outside")
            try:
                (models_dir / "linked.bin").symlink_to(outside_file)
            except OSError as error:
                self.skipTest(f"file symlinks are unavailable: {error}")

            config = inventory_artifacts.Config(
                models_dir=models_dir,
                resources_dir=resources_dir,
                results_dir=root / "results",
                progress_file=root / "results" / "progress",
                task_id="task-test",
                results_location="test-org/test-model",
                workflow_request={"workflowId": "workflow-test"},
            )
            with self.assertRaisesRegex(
                ValueError,
                "artifact path is not a regular file: linked.bin",
            ):
                inventory_artifacts.build_report(config)

    def test_build_report_rejects_directory_symlinks(self):
        with tempfile.TemporaryDirectory() as temporary_directory:
            root = Path(temporary_directory)
            models_dir = root / "models"
            resources_dir = root / "resources"
            models_dir.mkdir()
            resources_dir.mkdir()
            (models_dir / "weights.bin").write_bytes(b"model")
            (resources_dir / "eval.jsonl").write_text(
                '{"prompt":"hello"}\n',
                encoding="utf-8",
            )
            outside_directory = root / "outside"
            outside_directory.mkdir()
            (outside_directory / "hidden.bin").write_bytes(b"outside")
            try:
                (models_dir / "linked").symlink_to(
                    outside_directory,
                    target_is_directory=True,
                )
            except OSError as error:
                self.skipTest(f"directory symlinks are unavailable: {error}")

            config = inventory_artifacts.Config(
                models_dir=models_dir,
                resources_dir=resources_dir,
                results_dir=root / "results",
                progress_file=root / "results" / "progress",
                task_id="task-test",
                results_location="test-org/test-model",
                workflow_request={"workflowId": "workflow-test"},
            )
            with self.assertRaisesRegex(
                ValueError,
                "artifact path is not a regular directory: linked",
            ):
                inventory_artifacts.build_report(config)

    def test_load_config_decodes_workflow_request(self):
        request = {"workflowId": "workflow-test", "operation": "inventory-artifacts"}
        encoded_request = base64.b64encode(json.dumps(request).encode()).decode()
        with mock.patch.dict(
            os.environ,
            {
                "WORKFLOW_REQUEST_BASE64": encoded_request,
                "INPUT_MODELS_DIR": "/config/models/model",
                "INPUT_RESOURCES_DIR": "/config/resources/dataset",
                "NVCT_TASK_ID": "task-test",
                "RESULTS_LOCATION": "test-org/test-model",
            },
            clear=True,
        ):
            config = inventory_artifacts.load_config()

        self.assertEqual(config.workflow_request, request)
        self.assertEqual(config.models_dir, Path("/config/models/model"))
        self.assertEqual(config.resources_dir, Path("/config/resources/dataset"))
        self.assertEqual(config.task_id, "task-test")
        self.assertEqual(config.results_location, "test-org/test-model")

    def test_load_config_rejects_non_object_request(self):
        encoded_request = base64.b64encode(b'["not", "an", "object"]').decode()
        with mock.patch.dict(
            os.environ,
            {
                "WORKFLOW_REQUEST_BASE64": encoded_request,
                "RESULTS_LOCATION": "test-org/test-model",
            },
            clear=True,
        ):
            with self.assertRaisesRegex(ValueError, "must encode a JSON object"):
                inventory_artifacts.load_config()

    def test_load_config_reports_missing_required_environment(self):
        request = {"workflowId": "workflow-test"}
        environment = {
            "WORKFLOW_REQUEST_BASE64": base64.b64encode(
                json.dumps(request).encode()
            ).decode(),
            "RESULTS_LOCATION": "test-org/test-model",
        }
        for missing_name in ("WORKFLOW_REQUEST_BASE64", "RESULTS_LOCATION"):
            with self.subTest(missing_name=missing_name):
                incomplete_environment = environment.copy()
                incomplete_environment.pop(missing_name)
                with mock.patch.dict(os.environ, incomplete_environment, clear=True):
                    with self.assertRaisesRegex(
                        ValueError,
                        f"required environment variable {missing_name} is not set",
                    ):
                        inventory_artifacts.load_config()

    def test_load_config_rejects_empty_required_environment(self):
        request = {"workflowId": "workflow-test"}
        environment = {
            "WORKFLOW_REQUEST_BASE64": base64.b64encode(
                json.dumps(request).encode()
            ).decode(),
            "RESULTS_LOCATION": "test-org/test-model",
        }
        for empty_name in ("WORKFLOW_REQUEST_BASE64", "RESULTS_LOCATION"):
            with self.subTest(empty_name=empty_name):
                empty_environment = environment.copy()
                empty_environment[empty_name] = "   "
                with mock.patch.dict(os.environ, empty_environment, clear=True):
                    with self.assertRaisesRegex(
                        ValueError,
                        f"required environment variable {empty_name} is empty",
                    ):
                        inventory_artifacts.load_config()


if __name__ == "__main__":
    unittest.main()
