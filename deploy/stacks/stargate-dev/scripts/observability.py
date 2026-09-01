#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import json
import os
import sys
import tempfile
import time
from pathlib import Path

import yaml

import deploy


AMP_ALIAS = "stargate-dev"
WRITER_ROLE_NAME = "stargate-dev-amp-writer"
GRAFANA_ROLE_NAME = "stargate-dev-grafana"
ALLOY_SERVICE_ACCOUNT = "stargate-dev-alloy"
GRAFANA_SERVICE_ACCOUNT = "stargate-dev-grafana"


def aws(service: str, operation: str, *arguments: str) -> dict:
    result = deploy.run(["aws", service, operation, *arguments, "--output", "json"])
    if not result.stdout.strip():
        return {}
    value = json.loads(result.stdout)
    if not isinstance(value, dict):
        raise deploy.DeploymentError(
            f"unexpected response from aws {service} {operation}"
        )
    return value


def wait_for_amp(region: str, workspace_id: str) -> dict:
    for _ in range(60):
        workspace = aws(
            "amp",
            "describe-workspace",
            "--region",
            region,
            "--workspace-id",
            workspace_id,
        )["workspace"]
        status = workspace["status"]["statusCode"]
        if status == "ACTIVE":
            return workspace
        if status in {"CREATION_FAILED", "UPDATE_FAILED", "DELETING"}:
            raise deploy.DeploymentError(
                f"AMP workspace {workspace_id} entered {status}"
            )
        time.sleep(2)
    raise deploy.DeploymentError(f"AMP workspace {workspace_id} did not become active")


def ensure_amp(region: str) -> dict:
    workspaces = aws("amp", "list-workspaces", "--region", region).get(
        "workspaces", []
    )
    matches = [
        workspace for workspace in workspaces if workspace.get("alias") == AMP_ALIAS
    ]
    if len(matches) > 1:
        raise deploy.DeploymentError(f"multiple AMP workspaces use alias {AMP_ALIAS}")
    if matches:
        return wait_for_amp(region, matches[0]["workspaceId"])

    created = aws(
        "amp",
        "create-workspace",
        "--region",
        region,
        "--alias",
        AMP_ALIAS,
        "--tags",
        json.dumps({"Environment": "dev", "Service": "stargate"}),
    )
    return wait_for_amp(region, created["workspaceId"])


def oidc_provider(config: dict, cluster: dict, account: str) -> tuple[str, str]:
    issuer = aws(
        "eks",
        "describe-cluster",
        "--region",
        config["region"],
        "--name",
        cluster["name"],
    )["cluster"]["identity"]["oidc"]["issuer"].removeprefix("https://")
    provider_arn = f"arn:aws:iam::{account}:oidc-provider/{issuer}"
    try:
        aws(
            "iam",
            "get-open-id-connect-provider",
            "--open-id-connect-provider-arn",
            provider_arn,
        )
    except deploy.DeploymentError as error:
        raise deploy.DeploymentError(
            f"EKS cluster {cluster['name']} has no IAM OIDC provider: {provider_arn}"
        ) from error
    return issuer, provider_arn


def writer_trust_policy(config: dict, account: str) -> dict:
    namespace = config["observability"]["namespace"]
    clusters = [config["clusters"]["stargate"], *config["clusters"]["mockdcs"]]
    statements = []
    for cluster in clusters:
        issuer, provider_arn = oidc_provider(config, cluster, account)
        statements.append(
            {
                "Effect": "Allow",
                "Principal": {"Federated": provider_arn},
                "Action": "sts:AssumeRoleWithWebIdentity",
                "Condition": {
                    "StringEquals": {
                        f"{issuer}:aud": "sts.amazonaws.com",
                        f"{issuer}:sub": (
                            f"system:serviceaccount:{namespace}:"
                            f"{ALLOY_SERVICE_ACCOUNT}"
                        ),
                    }
                },
            }
        )
    return {"Version": "2012-10-17", "Statement": statements}


def grafana_trust_policy(config: dict, account: str) -> dict:
    issuer, provider_arn = oidc_provider(
        config, config["clusters"]["stargate"], account
    )
    subject = (
        f"system:serviceaccount:{config['observability']['namespace']}:"
        f"{GRAFANA_SERVICE_ACCOUNT}"
    )
    return {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {"Federated": provider_arn},
                "Action": "sts:AssumeRoleWithWebIdentity",
                "Condition": {
                    "StringEquals": {
                        f"{issuer}:aud": "sts.amazonaws.com",
                        f"{issuer}:sub": subject,
                    }
                },
            }
        ],
    }


def ensure_role(name: str, trust_policy: dict, permissions: dict) -> str:
    trust = json.dumps(trust_policy, separators=(",", ":"))
    try:
        role = aws("iam", "get-role", "--role-name", name)["Role"]
        aws(
            "iam",
            "update-assume-role-policy",
            "--role-name",
            name,
            "--policy-document",
            trust,
        )
    except deploy.DeploymentError as error:
        if "NoSuchEntity" not in str(error):
            raise
        role = aws(
            "iam",
            "create-role",
            "--role-name",
            name,
            "--assume-role-policy-document",
            trust,
            "--description",
            "Stargate dev observability",
            "--tags",
            "Key=Environment,Value=dev",
            "Key=Service,Value=stargate",
        )["Role"]
    aws(
        "iam",
        "put-role-policy",
        "--role-name",
        name,
        "--policy-name",
        name,
        "--policy-document",
        json.dumps(permissions, separators=(",", ":")),
    )
    return role["Arn"]


def write_observability_values(
    path: Path,
    amp_workspace_id: str,
    writer_role_arn: str,
    grafana_role_arn: str,
) -> None:
    path = path.expanduser().resolve()
    values = deploy.load_yaml(path)
    observability = values.setdefault("observability", {})
    if not isinstance(observability, dict):
        raise deploy.DeploymentError("observability values must be an object")
    observability["amp"] = {
        "workspaceId": amp_workspace_id,
        "writerRoleArn": writer_role_arn,
    }
    observability["grafana"] = {"readerRoleArn": grafana_role_arn}

    descriptor, temporary_name = tempfile.mkstemp(
        prefix=f".{path.name}.", dir=path.parent
    )
    temporary = Path(temporary_name)
    try:
        os.fchmod(descriptor, 0o600)
        with os.fdopen(descriptor, "w", encoding="utf-8") as file:
            yaml.safe_dump(values, file, sort_keys=False)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def apply(region: str, values_path: Path) -> None:
    deploy.require_tools("aws", "helm", "helmfile", "kubeconform", "kubectl")
    config = deploy.load_deployment_values(region, values_path)
    account = deploy.aws_account(config)
    amp = ensure_amp(region)

    writer_role_arn = ensure_role(
        WRITER_ROLE_NAME,
        writer_trust_policy(config, account),
        {
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Action": "aps:RemoteWrite",
                    "Resource": amp["arn"],
                }
            ],
        },
    )
    grafana_role_arn = ensure_role(
        GRAFANA_ROLE_NAME,
        grafana_trust_policy(config, account),
        {
            "Version": "2012-10-17",
            "Statement": [
                {
                    "Effect": "Allow",
                    "Action": [
                        "aps:QueryMetrics",
                        "aps:GetSeries",
                        "aps:GetLabels",
                        "aps:GetMetricMetadata",
                    ],
                    "Resource": amp["arn"],
                }
            ],
        },
    )
    write_observability_values(
        values_path, amp["workspaceId"], writer_role_arn, grafana_role_arn
    )
    deploy.apply(region, "observability", None, values_path)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Provision and deploy Stargate dev metrics for one region"
    )
    parser.add_argument("--region", required=True)
    parser.add_argument("--values", type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    os.umask(0o077)
    args = parse_args()
    try:
        apply(args.region, args.values)
        print(
            "Grafana is available through service "
            "stargate-dev-observability/stargate-dev-grafana"
        )
    except (
        deploy.DeploymentError,
        json.JSONDecodeError,
        OSError,
        yaml.YAMLError,
    ) as error:
        print(f"error: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
