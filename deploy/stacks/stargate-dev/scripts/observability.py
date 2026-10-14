#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import json
import sys
import time
from pathlib import Path

import yaml

import deploy


AMP_ALIAS = "stargate-dev"
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
    workspaces = aws("amp", "list-workspaces", "--region", region).get("workspaces", [])
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


def trust_policy(
    config: dict, account: str, clusters: list[dict], service_account: str
) -> dict:
    subject = (
        f"system:serviceaccount:{config['observability']['namespace']}:"
        f"{service_account}"
    )
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
                        f"{issuer}:sub": subject,
                    }
                },
            }
        )
    return {"Version": "2012-10-17", "Statement": statements}


def ensure_role(name: str, trust_policy: dict, permissions: dict) -> None:
    trust = json.dumps(trust_policy, separators=(",", ":"))
    try:
        aws("iam", "get-role", "--role-name", name)
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
        aws(
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
        )
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


def provision(region: str, values_path: Path) -> dict:
    config = deploy.load_deployment_values(region, values_path)
    if deploy.is_local(config):
        raise deploy.DeploymentError(
            "observability is not supported for local environments"
        )
    deploy.require_tools("aws")
    account = deploy.aws_account()
    stargate = config["clusters"]["stargate"]
    writer_trust = trust_policy(
        config,
        account,
        [stargate, *config["clusters"]["mockdcs"]],
        ALLOY_SERVICE_ACCOUNT,
    )
    grafana_trust = trust_policy(config, account, [stargate], GRAFANA_SERVICE_ACCOUNT)
    writer_role_name, grafana_role_name = deploy.role_names(region)

    amp = ensure_amp(region)
    ensure_role(
        writer_role_name,
        writer_trust,
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
    ensure_role(
        grafana_role_name,
        grafana_trust,
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
    # The helmfile derives both role ARNs from the account and region.
    return {"observability": {"amp": {"workspaceId": amp["workspaceId"]}}}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Provision the AMP workspace and IAM roles for one region"
    )
    parser.add_argument("--region", required=True)
    parser.add_argument("--values", type=Path, required=True)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        values = provision(args.region, args.values)
        print(f"# Merge into {args.values}, then apply the observability phase.")
        print(yaml.safe_dump(values, sort_keys=False), end="")
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
