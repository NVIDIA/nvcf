#!/usr/bin/env python3
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

import argparse
import json
import os
import re
import secrets
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

import yaml

import deploy


STACK_DIR = Path(__file__).resolve().parents[1]
DASHBOARD_DIR = STACK_DIR / "dashboards"
AMP_ALIAS = "stargate-dev"
GRAFANA_NAME = "stargate-dev"
GRAFANA_VERSION = "12.4"
WRITER_ROLE_NAME = "stargate-dev-amp-writer"
GRAFANA_ROLE_NAME = "stargate-dev-grafana"
ALLOY_SERVICE_ACCOUNT = "stargate-dev-alloy"
DATASOURCE_UID = "stargate-dev-amp"


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
                            f"system:serviceaccount:{namespace}:{ALLOY_SERVICE_ACCOUNT}"
                        ),
                    }
                },
            }
        )
    return {"Version": "2012-10-17", "Statement": statements}


def grafana_trust_policy(region: str, account: str) -> dict:
    return {
        "Version": "2012-10-17",
        "Statement": [
            {
                "Effect": "Allow",
                "Principal": {"Service": "grafana.amazonaws.com"},
                "Action": "sts:AssumeRole",
                "Condition": {
                    "StringEquals": {"aws:SourceAccount": account},
                    "ArnLike": {
                        "aws:SourceArn": f"arn:aws:grafana:{region}:{account}:/workspaces/*"
                    },
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


def wait_for_grafana(region: str, workspace_id: str) -> dict:
    for _ in range(90):
        workspace = aws(
            "grafana",
            "describe-workspace",
            "--region",
            region,
            "--workspace-id",
            workspace_id,
        )["workspace"]
        status = workspace["status"]
        if status == "ACTIVE":
            return workspace
        if status in {"CREATION_FAILED", "UPDATE_FAILED", "DELETING"}:
            raise deploy.DeploymentError(
                f"Grafana workspace {workspace_id} entered {status}"
            )
        time.sleep(2)
    raise deploy.DeploymentError(
        f"Grafana workspace {workspace_id} did not become active"
    )


def ensure_grafana(region: str, role_arn: str) -> dict:
    summaries = aws("grafana", "list-workspaces", "--region", region).get(
        "workspaces", []
    )
    matches = [
        workspace for workspace in summaries if workspace.get("name") == GRAFANA_NAME
    ]
    if len(matches) > 1:
        raise deploy.DeploymentError(
            f"multiple Grafana workspaces use name {GRAFANA_NAME}"
        )
    if matches:
        workspace = wait_for_grafana(region, matches[0]["id"])
        if workspace.get("grafanaVersion") != GRAFANA_VERSION:
            raise deploy.DeploymentError(
                f"existing Grafana workspace must run version {GRAFANA_VERSION}"
            )
        if workspace.get("permissionType") != "CUSTOMER_MANAGED":
            raise deploy.DeploymentError(
                "existing Grafana workspace must use customer-managed permissions"
            )
        if workspace.get("workspaceRoleArn") != role_arn:
            aws(
                "grafana",
                "update-workspace",
                "--region",
                region,
                "--workspace-id",
                workspace["id"],
                "--workspace-role-arn",
                role_arn,
            )
            workspace = wait_for_grafana(region, workspace["id"])
        return workspace

    created = aws(
        "grafana",
        "create-workspace",
        "--region",
        region,
        "--workspace-name",
        GRAFANA_NAME,
        "--workspace-description",
        "Global metrics for Stargate dev clusters",
        "--account-access-type",
        "CURRENT_ACCOUNT",
        "--authentication-providers",
        "AWS_SSO",
        "--permission-type",
        "CUSTOMER_MANAGED",
        "--workspace-role-arn",
        role_arn,
        "--grafana-version",
        GRAFANA_VERSION,
        "--tags",
        json.dumps({"Environment": "dev", "Service": "stargate"}),
    )["workspace"]
    return wait_for_grafana(region, created["id"])


def current_sso_username(identity: dict) -> str:
    username = identity.get("Arn", "").rsplit("/", 1)[-1]
    if not re.fullmatch(r"[^@/]+@[^@/]+", username):
        raise deploy.DeploymentError(
            "could not infer the IAM Identity Center username; pass --sso-user"
        )
    return username


def grant_admin(region: str, workspace_id: str, username: str) -> None:
    instances = aws("sso-admin", "list-instances", "--region", region).get(
        "Instances", []
    )
    if len(instances) != 1:
        raise deploy.DeploymentError(
            "expected exactly one IAM Identity Center instance"
        )
    store_id = instances[0]["IdentityStoreId"]
    users = aws(
        "identitystore",
        "list-users",
        "--region",
        region,
        "--identity-store-id",
        store_id,
        "--filters",
        f"AttributePath=UserName,AttributeValue={username}",
    ).get("Users", [])
    if len(users) != 1:
        raise deploy.DeploymentError(
            f"IAM Identity Center user {username} was not uniquely found"
        )
    user_id = users[0]["UserId"]
    permissions = aws(
        "grafana",
        "list-permissions",
        "--region",
        region,
        "--workspace-id",
        workspace_id,
    ).get("permissions", [])
    if any(
        item.get("user", {}).get("id") == user_id and item.get("role") == "ADMIN"
        for item in permissions
    ):
        return
    try:
        aws(
            "grafana",
            "update-permissions",
            "--region",
            region,
            "--workspace-id",
            workspace_id,
            "--update-instruction-batch",
            json.dumps(
                [
                    {
                        "action": "ADD",
                        "role": "ADMIN",
                        "users": [{"id": user_id, "type": "SSO_USER"}],
                    }
                ]
            ),
        )
    except deploy.DeploymentError:
        permissions = aws(
            "grafana",
            "list-permissions",
            "--region",
            region,
            "--workspace-id",
            workspace_id,
        ).get("permissions", [])
        if not any(
            item.get("user", {}).get("id") == user_id and item.get("role") == "ADMIN"
            for item in permissions
        ):
            raise


def write_observability_values(
    path: Path, amp_workspace_id: str, writer_role_arn: str, grafana: dict
) -> None:
    path = path.expanduser().resolve()
    values = deploy.load_yaml(path)
    values["observability"] = {
        "amp": {
            "workspaceId": amp_workspace_id,
            "writerRoleArn": writer_role_arn,
        },
        "grafana": {
            "workspaceId": grafana["id"],
            "endpoint": grafana["endpoint"],
        },
    }
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


def grafana_request(
    endpoint: str,
    token: str,
    method: str,
    path: str,
    payload: dict | None = None,
) -> dict:
    data = None if payload is None else json.dumps(payload).encode()
    request = urllib.request.Request(
        f"https://{endpoint}{path}",
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            body = response.read()
    except urllib.error.HTTPError as error:
        body = error.read().decode(errors="replace")
        raise deploy.DeploymentError(
            f"Grafana API {method} {path} returned {error.code}: {body}"
        ) from error
    if not body:
        return {}
    value = json.loads(body)
    return value if isinstance(value, dict) else {"value": value}


def wait_for_metrics(endpoint: str, token: str, region: str) -> None:
    query = urllib.parse.quote(f'count(up{{region="{region}"}})')
    for _ in range(12):
        response = grafana_request(
            endpoint,
            token,
            "GET",
            f"/api/datasources/proxy/uid/{DATASOURCE_UID}/api/v1/query?query={query}",
        )
        results = response.get("data", {}).get("result", [])
        if results and float(results[0]["value"][1]) > 0:
            return
        time.sleep(5)
    raise deploy.DeploymentError("Grafana could not query collected metrics from AMP")


def provision_grafana(region: str, workspace: dict, amp_workspace_id: str) -> None:
    workspace_id = workspace["id"]
    name = f"stargate-dev-provisioner-{secrets.token_hex(4)}"
    service_account_id = None
    token_id = None
    try:
        service_account = aws(
            "grafana",
            "create-workspace-service-account",
            "--region",
            region,
            "--workspace-id",
            workspace_id,
            "--name",
            name,
            "--grafana-role",
            "ADMIN",
        )
        service_account_id = service_account["id"]
        token_response = aws(
            "grafana",
            "create-workspace-service-account-token",
            "--region",
            region,
            "--workspace-id",
            workspace_id,
            "--service-account-id",
            service_account_id,
            "--name",
            name,
            "--seconds-to-live",
            "900",
        )
        token = token_response["serviceAccountToken"]
        token_id = token["id"]
        token_key = token["key"]

        datasource = {
            "name": "Stargate dev AMP",
            "uid": DATASOURCE_UID,
            "type": "grafana-amazonprometheus-datasource",
            "access": "proxy",
            "url": (
                f"https://aps-workspaces.{region}.amazonaws.com/workspaces/"
                f"{amp_workspace_id}"
            ),
            "isDefault": True,
            "editable": False,
            "jsonData": {
                "httpMethod": "POST",
                "sigV4Auth": True,
                "sigV4AuthType": "default",
                "sigV4Region": region,
                "sigv4Service": "aps",
                "manageAlerts": False,
            },
        }
        try:
            existing = grafana_request(
                workspace["endpoint"],
                token_key,
                "GET",
                f"/api/datasources/uid/{DATASOURCE_UID}",
            )
        except deploy.DeploymentError as error:
            if "returned 404" not in str(error):
                raise
            existing = None
        if existing:
            grafana_request(
                workspace["endpoint"],
                token_key,
                "PUT",
                f"/api/datasources/uid/{DATASOURCE_UID}",
                datasource,
            )
        else:
            grafana_request(
                workspace["endpoint"], token_key, "POST", "/api/datasources", datasource
            )

        health = grafana_request(
            workspace["endpoint"],
            token_key,
            "GET",
            f"/api/datasources/uid/{DATASOURCE_UID}/health",
        )
        if health.get("status") != "OK":
            raise deploy.DeploymentError(
                f"Grafana AMP data source is unhealthy: {health.get('message', health)}"
            )
        wait_for_metrics(workspace["endpoint"], token_key, region)

        for dashboard_path in sorted(DASHBOARD_DIR.glob("*.json")):
            with dashboard_path.open(encoding="utf-8") as file:
                dashboard = json.load(file)
            grafana_request(
                workspace["endpoint"],
                token_key,
                "POST",
                "/api/dashboards/db",
                {"dashboard": dashboard, "folderUid": "", "overwrite": True},
            )
            saved = grafana_request(
                workspace["endpoint"],
                token_key,
                "GET",
                f"/api/dashboards/uid/{dashboard['uid']}",
            )
            if saved.get("dashboard", {}).get("uid") != dashboard["uid"]:
                raise deploy.DeploymentError(
                    f"Grafana dashboard {dashboard['uid']} was not saved"
                )
    finally:
        try:
            if token_id and service_account_id:
                aws(
                    "grafana",
                    "delete-workspace-service-account-token",
                    "--region",
                    region,
                    "--workspace-id",
                    workspace_id,
                    "--service-account-id",
                    service_account_id,
                    "--token-id",
                    token_id,
                )
        finally:
            if service_account_id:
                aws(
                    "grafana",
                    "delete-workspace-service-account",
                    "--region",
                    region,
                    "--workspace-id",
                    workspace_id,
                    "--service-account-id",
                    service_account_id,
                )


def apply(
    region: str, credentials_path: Path, values_path: Path, sso_user: str | None
) -> str:
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
        grafana_trust_policy(region, account),
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
    grafana = ensure_grafana(region, grafana_role_arn)
    identity = aws("sts", "get-caller-identity")
    grant_admin(region, grafana["id"], sso_user or current_sso_username(identity))
    write_observability_values(
        values_path, amp["workspaceId"], writer_role_arn, grafana
    )
    deploy.apply(region, "observability", credentials_path, values_path)
    provision_grafana(region, grafana, amp["workspaceId"])
    return f"https://{grafana['endpoint']}"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Provision and deploy Stargate dev metrics for one region"
    )
    parser.add_argument("--region", required=True)
    parser.add_argument("--credentials", type=Path, required=True)
    parser.add_argument("--values", type=Path, required=True)
    parser.add_argument("--sso-user")
    return parser.parse_args()


def main() -> int:
    os.umask(0o077)
    args = parse_args()
    try:
        url = apply(args.region, args.credentials, args.values, args.sso_user)
        print(f"Grafana: {url}")
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
