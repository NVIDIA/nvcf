# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import base64
import datetime
import hashlib
import json
import os
import stat
from dataclasses import dataclass
from pathlib import Path


@dataclass(frozen=True)
class Config:
    models_dir: Path
    resources_dir: Path
    results_dir: Path
    progress_file: Path
    task_id: str
    results_location: str
    workflow_request: dict


def load_config() -> Config:
    encoded_request = os.environ["WORKFLOW_REQUEST_BASE64"]
    try:
        decoded_request = base64.b64decode(encoded_request, validate=True)
        workflow_request = json.loads(decoded_request)
    except (ValueError, json.JSONDecodeError) as error:
        raise ValueError("WORKFLOW_REQUEST_BASE64 must encode a JSON object") from error
    if not isinstance(workflow_request, dict):
        raise ValueError("WORKFLOW_REQUEST_BASE64 must encode a JSON object")

    results_dir = Path(os.getenv("NVCT_RESULTS_DIR", "/var/task/result"))
    return Config(
        models_dir=Path(os.getenv("INPUT_MODELS_DIR", "/config/models")),
        resources_dir=Path(os.getenv("INPUT_RESOURCES_DIR", "/config/resources")),
        results_dir=results_dir,
        progress_file=Path(
            os.getenv("NVCT_PROGRESS_FILE_PATH", str(results_dir / "progress"))
        ),
        task_id=os.getenv("NVCT_TASK_ID", ""),
        results_location=os.environ["RESULTS_LOCATION"],
        workflow_request=workflow_request,
    )


def hash_artifacts(root: Path) -> list[dict]:
    artifacts = []
    if not root.is_dir():
        return artifacts

    for current_root, directory_names, file_names in os.walk(root):
        for directory_name in directory_names:
            directory_path = Path(current_root, directory_name)
            if directory_path.is_symlink():
                relative_path = directory_path.relative_to(root).as_posix()
                raise ValueError(
                    f"artifact path is not a regular directory: {relative_path}"
                )
        directory_names.sort()
        for file_name in sorted(file_names):
            path = Path(current_root, file_name)
            relative_path = path.relative_to(root)
            if relative_path == Path(".nvcf_manifest.json"):
                continue
            file_metadata = path.lstat()
            if not stat.S_ISREG(file_metadata.st_mode):
                raise ValueError(
                    "artifact path is not a regular file: "
                    f"{relative_path.as_posix()}"
                )
            open_flags = os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0)
            file_descriptor = os.open(path, open_flags)
            digest = hashlib.sha256()
            with os.fdopen(file_descriptor, "rb") as artifact_file:
                opened_metadata = os.fstat(artifact_file.fileno())
                if (
                    not stat.S_ISREG(opened_metadata.st_mode)
                    or opened_metadata.st_dev != file_metadata.st_dev
                    or opened_metadata.st_ino != file_metadata.st_ino
                ):
                    raise ValueError(
                        "artifact path changed while opening: "
                        f"{relative_path.as_posix()}"
                    )
                for chunk in iter(lambda: artifact_file.read(1024 * 1024), b""):
                    digest.update(chunk)
            artifacts.append(
                {
                    "path": relative_path.as_posix(),
                    "sizeBytes": opened_metadata.st_size,
                    "sha256": digest.hexdigest(),
                }
            )
    return artifacts


def build_report(config: Config) -> dict:
    models = hash_artifacts(config.models_dir)
    datasets = hash_artifacts(config.resources_dir)
    if not models:
        raise ValueError(f"no model files found under {config.models_dir}")
    if not datasets:
        raise ValueError(f"no dataset files found under {config.resources_dir}")

    return {
        "workflowRequest": config.workflow_request,
        "modelArtifacts": models,
        "datasetArtifacts": datasets,
        "summary": {
            "modelFileCount": len(models),
            "datasetFileCount": len(datasets),
            "totalBytes": sum(
                artifact["sizeBytes"] for artifact in models + datasets
            ),
        },
    }


def write_json(path: Path, value: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_path = path.with_name(f"{path.name}.tmp")
    temporary_path.write_text(
        json.dumps(value, indent=2, sort_keys=True) + "\n",
        encoding="utf-8",
    )
    os.replace(temporary_path, path)


def run(config: Config) -> tuple[Path, dict]:
    report = build_report(config)
    output_name = "artifact-inventory"
    report_path = config.results_dir / output_name / "report.json"
    write_json(report_path, report)

    report_digest = hashlib.sha256(report_path.read_bytes()).hexdigest()
    metadata = {
        "status": "complete",
        "workflowId": config.workflow_request.get("workflowId", ""),
        "resultsLocation": config.results_location,
        "reportPath": "report.json",
        **report["summary"],
        "reportSha256": report_digest,
    }
    progress = {
        "taskId": config.task_id,
        "percentComplete": 100,
        "name": output_name,
        "lastUpdatedAt": datetime.datetime.now(datetime.timezone.utc)
        .isoformat(timespec="microseconds")
        .replace("+00:00", "Z"),
        "metadata": metadata,
    }
    write_json(config.progress_file, progress)
    return report_path, metadata


def main() -> None:
    report_path, metadata = run(load_config())
    print(
        json.dumps(
            {
                "report": str(report_path),
                "metadata": metadata,
            },
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    main()
