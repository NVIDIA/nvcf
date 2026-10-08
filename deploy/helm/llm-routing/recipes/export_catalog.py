#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES.
# SPDX-License-Identifier: Apache-2.0
"""Export the common recipe index from recipe-owned, pinned metadata."""
import argparse
import copy
import json
from pathlib import Path
import re
from urllib.parse import urlsplit

HERE = Path(__file__).resolve().parent
GIB = 1 << 30


def read(path):
    return json.loads(path.read_text())


def chart_reference(root, relative, repository=None):
    """localPath is relative to the index directory and names an archive after packaging."""
    chart = root / relative
    text = (chart / "Chart.yaml").read_text()
    fields = {}
    for key in ("name", "version"):
        match = re.search(r"^" + key + r":\s*([^\s#]+)", text, re.MULTILINE)
        if not match:
            raise ValueError(f"Missing {key} in {chart}/Chart.yaml")
        fields[key] = match.group(1).strip("\"'")
    if repository is not None:
        message = "chartRepository must be a full OCI chart repository ending in the chart name, without credentials, a tag or digest."
        if not isinstance(repository, str) or re.search(r"[\s\x00-\x1f\x7f@?#]", repository):
            raise ValueError(message)
        parsed = urlsplit(repository)
        host = r"[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?"
        component = r"[a-z0-9]+(?:(?:[._]|__|[-]+)[a-z0-9]+)*"
        if (parsed.scheme != "oci" or not repository.startswith("oci://")
                or not re.fullmatch(r"(?:" + host + r"\.)*" + host + r"(?::[0-9]+)?", parsed.netloc)
                or (parsed.port is not None and not 1 <= parsed.port <= 65535)
                or not re.fullmatch(r"(?:/" + component + r")+", parsed.path)
                or parsed.path.rsplit("/", 1)[-1] != fields["name"]):
            raise ValueError(message)
    return {**fields, "localPath": str(relative),
            "archive": f"{fields['name']}-{fields['version']}.tgz", "repository": repository,
            "publication": "oci" if repository is not None else "local"}


def bytes_gib(value):
    return int(value * GIB)


def resource_requirements(cpu_request, memory_request, memory_limit, gpus, cpu_limit=None):
    return {"cpuRequestMillicores": int(float(cpu_request) * 1000),
            "cpuLimitMillicores": int(float(cpu_limit) * 1000) if cpu_limit else None,
            "memoryRequestBytes": bytes_gib(memory_request),
            "memoryLimitBytes": bytes_gib(memory_limit),
            "gpuRequest": gpus, "gpuLimit": gpus}


def normalized_resources(resources):
    def memory(value):
        match = re.fullmatch(r"([0-9]+)(Mi|Gi)", str(value))
        if not match:
            raise ValueError("Profile memory must use explicit Mi or Gi units")
        return int(match[1]) * {"Mi": 1 << 20, "Gi": GIB}[match[2]]
    def cpu(value):
        value = str(value)
        return int(value[:-1]) if value.endswith("m") else int(float(value) * 1000)
    requests, limits = resources["requests"], resources["limits"]
    return {"cpuRequestMillicores": cpu(requests["cpu"]), "cpuLimitMillicores": cpu(limits["cpu"]),
            "memoryRequestBytes": memory(requests["memory"]), "memoryLimitBytes": memory(limits["memory"]),
            "gpuRequest": requests.get("nvidia.com/gpu", 0), "gpuLimit": limits.get("nvidia.com/gpu", 0)}


def helm_validation(record, profile, revision, image):
    if not record or record.get("profile") != profile:
        return {"automaticHelmStatus": "pending", "automaticHelmWorkload": None}
    if record.get("modelRevision") != revision or record.get("runtimeImage") != image:
        raise ValueError("Automatic Helm validation pins changed. Revalidate the profile or clear its old validation record.")
    status = record.get("status", "smoke-tested")
    if status not in ("smoke-tested", "failed"):
        raise ValueError("Unsupported automatic Helm validation status")
    return {"automaticHelmStatus": status, "automaticHelmWorkload": copy.deepcopy(record)}


def profile_validation(model, profile, key):
    record = profile.get(key, model.get(key))
    if record and record.get("profile") != profile["id"]:
        if key in profile:
            raise ValueError("Validation record belongs to another profile: " + profile["id"])
        return None
    return copy.deepcopy(record)


def sglang_recipes(root):
    automatic_ids = set(read(root / "charts/sglang/files/profiles.json"))
    recipes = []
    for model in read(root / "catalog.json")["models"]:
        chart = chart_reference(root, Path("charts/sglang"), model.get("chartRepository"))
        if model["id"] not in automatic_ids:
            raise ValueError("Catalog recipe is missing its Helm runtime: " + model["id"])
        profiles = []
        for profile in model["profiles"]:
            hardware = copy.deepcopy(profile["hardware"])
            validated = profile_validation(model, profile, "validatedWorkload")
            if validated:
                validated.setdefault("requestType", "short-prompt-smoke")
                validated.setdefault("fullContextExercised", False)
            storage = {"claimRequestBytes": bytes_gib(profile["cacheGiB"]),
                       "minimumFreeBytesBeforeDownload": bytes_gib(profile["minFreeDiskGiB"])}
            if profile.get("offload"):
                storage["offloadBytes"] = bytes_gib(profile["offloadGiB"])
                storage["offloadMedium"] = "local-nvme"
            profiles.append({
                "id": profile["id"], "hardware": hardware,
                "modelNodeCount": profile["nodes"], "gpusPerNode": hardware["gpuCount"],
                "perNode": [{"rank": rank, "role": "model",
                    "resources": resource_requirements(profile["cpu"], profile["memoryGiB"],
                        profile["memoryLimitGiB"], hardware["gpuCount"]), "storage": copy.deepcopy(storage)}
                    for rank in range(profile["nodes"])],
                "fabric": {"minimumGbps": profile["minFabricGbps"]} if profile.get("fabric") else None,
                "workload": {"defaultContextTokens": 8192, "defaultConcurrency": 1,
                    "maximumContextTokens": profile["maxContext"], "maximumConcurrency": profile["maxConcurrency"]},
                "validation": {"runtimeStatus": "smoke-tested" if validated else "pending",
                    "workload": validated, **helm_validation(profile_validation(model, profile, "automaticHelmValidation"), profile["id"],
                        model["revision"], model["image"])},
                "deployment": {"chart": copy.deepcopy(chart), "releaseName": model["releaseName"],
                    "lifecycle": "automatic",
                    "values": {"recipe": model["id"], "profileName": profile["id"]},
                    "requiredSiteValues": ["nodes", "runtimeClassName", "storageClassName", "sharedCAConfigMap"]
                        + (["nodeCapabilities"] if profile.get("offload") or profile.get("fabric") else []),
                    "guide": ("../ADVANCED.md#helm-flash-next-recipes" if model["id"] == "qwen3.8-flash-next"
                        else "../README.md")},
            })
        recipes.append({"id": model["id"], "name": model["name"], "servedModelId": model["id"],
            "availability": {"status": "available", "deployable": True},
            "precision": model["precision"], "license": model["license"],
            "licenseNotice": "notices/" + model["id"] + "/NOTICE",
            "model": {"repository": model["repository"], "revision": model["revision"], "weightBytes": None},
            "runtime": {"backend": "sglang", "version": model["runtime"], "image": model["image"]},
            "profiles": profiles})
    return recipes


def gguf_recipes(root):
    recipes = []
    for metadata_path in sorted(root.glob("*/profiles.json")):
        metadata = read(metadata_path)
        recipe = read(metadata_path.parent / metadata["recipeFile"])
        chart = chart_reference(root, Path("charts/gguf-backend"), recipe.get("chartRepository"))
        lock = read(metadata_path.parent / metadata["lockFile"])
        profiles = []
        for profile in metadata["profiles"]:
            tuning = recipe["tuning"][profile["hardware"]["memoryMode"]]
            if (profile["contextLength"], profile["concurrency"]) != (tuning["contextPerSlot"], tuning["slots"]):
                raise ValueError("Automatic profile context and concurrency must match recipe tuning: " + profile["id"])
            per_node = []
            for rank, role in enumerate(profile["roles"]):
                serving = normalized_resources(role["resources"])
                effective = copy.deepcopy(serving)
                preparation = normalized_resources(profile["preparation"]["resources"]) if rank == 0 else None
                if preparation:
                    effective = {key: max(value, preparation[key]) for key, value in effective.items()}
                node = {"rank": rank, "role": role["role"], "resources": effective,
                    "servingResources": serving,
                    "storage": {"claimRequestBytes": bytes_gib(role["storageGiB"])}}
                if preparation:
                    node["preparationResources"] = preparation
                    node["additionalWorkloads"] = [{"role": "artifacts", "sharesModelClaim": True,
                        "resources": normalized_resources(profile["artifactsService"]["resources"])}]
                per_node.append(node)
            profiles.append({"id": profile["id"], "hardware": copy.deepcopy(profile["hardware"]),
                "modelNodeCount": profile["nodes"], "gpusPerNode": profile["gpusPerNode"],
                "minimumAvailableMemoryBytesPerNode": bytes_gib(profile["minAvailableMemoryGiB"]),
                "perNode": per_node,
                "workload": {"defaultContextTokens": profile["contextLength"], "defaultConcurrency": profile["concurrency"],
                    "maximumContextTokens": profile["contextLength"], "maximumConcurrency": profile["concurrency"]},
                "validation": {"runtimeStatus": profile["validation"]["legacyWorkflow"],
                    **helm_validation(profile["validation"].get("automaticHelmValidation"), profile["id"],
                        lock["revision"], metadata["runtimeImage"]),
                    "scope": profile["validation"]["scope"],
                    "workload": {"contextLength": profile["contextLength"], "concurrency": profile["concurrency"],
                        "requestType": "short-prompt-smoke", "fullContextExercised": False,
                        "performanceBenchmarked": False, "qualityEvaluated": False}},
                "deployment": {"chart": copy.deepcopy(chart), "releaseName": recipe["releaseName"], "lifecycle": "automatic",
                    "values": {"recipe": metadata["recipe"], "profileName": profile["id"]},
                    "requiredSiteValues": ["nodes", "runtimeClassName", "storageClassName", "sharedCAConfigMap"],
                    "guide": "../ADVANCED.md#helm-glm-recipe"}})
        recipes.append({"id": metadata["recipe"], "name": recipe["name"], "servedModelId": recipe["servedName"],
            "availability": {"status": "available", "deployable": True},
            "precision": lock["quantization"], "license": "glm-5.3",
            "licenseNotice": "notices/" + metadata["recipe"] + "/NOTICE",
            "model": {"repository": lock["model"], "revision": lock["revision"], "weightBytes": lock["weightFileBytes"]},
            "runtime": {"backend": "llama.cpp", "revision": recipe["llamaCppRevision"], "image": metadata["runtimeImage"]},
            "profiles": profiles})
    return recipes


def planned_recipes(root):
    metadata = read(root / "planned.json")
    if metadata.get("schemaVersion") != 1:
        raise ValueError("Unsupported planned recipe metadata version")
    recipes = copy.deepcopy(metadata["recipes"])
    for recipe in recipes:
        availability = recipe.get("availability", {})
        if (availability.get("status") not in ("planned", "unavailable")
                or availability.get("deployable") is not False
                or recipe.get("servedModelId") is not None or recipe.get("profiles") != []
                or any(recipe.get(key) is not None for key in ("model", "runtime", "precision", "license", "deployment", "chart"))):
            raise ValueError("Planned recipes must not expose deployment profiles, pins or served model IDs")
        if not availability.get("reason") or not availability.get("sources") or not re.fullmatch(
                r"\d{4}-\d{2}-\d{2}", availability.get("checkedAt", "")):
            raise ValueError("Planned recipes require a dated reason and primary sources")
        for candidate in recipe.get("upstreamCandidates", []):
            if any(key in candidate for key in ("deployment", "profiles", "chart")):
                raise ValueError("Upstream candidates must not expose an installable deployment")
            if (not re.fullmatch(r"[0-9a-f]{40}", candidate["model"]["revision"])
                    or not re.fullmatch(r".+@sha256:[0-9a-f]{64}", candidate["runtime"]["image"])):
                raise ValueError("Upstream candidates require immutable model and runtime references")
    return recipes


def build(root=HERE):
    recipes = sglang_recipes(root) + gguf_recipes(root) + planned_recipes(root)
    for recipe in recipes:
        if recipe.get("licenseNotice") is not None:
            recipe["licenseNotice"] = "NOTICE"
    if len({recipe["id"] for recipe in recipes}) != len(recipes):
        raise ValueError("Duplicate recipe ID in common catalog")
    return {"schemaVersion": 1,
        "units": {"memory": "bytes", "storage": "bytes", "cpu": "millicores", "context": "tokens"},
        "resourceScope": "Per model pod scheduling reservation, including init containers. additionalWorkloads lists separate per-node pods. Shared gateway, operator and Pylon resources are additional.",
        "memoryAccounting": "Unified-memory profiles use one shared host/GPU memory pool. Do not add a separate GPU memory allocation.",
        "placement": "Choose an eligible profile and supply distinct compatible nodes with exclusive available GPUs.",
        "recipes": sorted(recipes, key=lambda recipe: recipe["id"])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=HERE / "index.json")
    parser.add_argument("--check", action="store_true", help="Fail when the committed index needs regeneration")
    args = parser.parse_args()
    content = json.dumps(build(), indent=2) + "\n"
    if args.check:
        if not args.output.exists() or args.output.read_text() != content:
            parser.exit(1, "Recipe index is stale. Run python3 export_catalog.py.\n")
        print("Recipe index matches its source metadata.")
    else:
        args.output.write_text(content)
        print(args.output)


if __name__ == "__main__":
    main()
