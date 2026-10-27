<!--
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
-->

# GDS IAM

Two roles touch the GDS DynamoDB table: the server role that gds-server
pods assume, and an admin role for `gds-admin` and Global Table changes.
Neither needs anything outside DynamoDB (and KMS, when the table uses a
customer managed key). Replace `<account>`, the regions and the table name
(`gds` by default) below.

## Server role

gds-server reads and writes the replica of its own region. One role is
usually shared by every region, so the policy lists the table ARN in each
replica region; a per-region role can list only its own.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "GdsTableReadWrite",
      "Effect": "Allow",
      "Action": [
        "dynamodb:GetItem",
        "dynamodb:Query",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem",
        "dynamodb:BatchWriteItem",
        "dynamodb:ConditionCheckItem"
      ],
      "Resource": [
        "arn:aws:dynamodb:us-east-1:<account>:table/gds",
        "arn:aws:dynamodb:us-west-2:<account>:table/gds"
      ]
    }
  ]
}
```

Why each action:

| Action | Used by |
| :---- | :---- |
| `GetItem` | `GetCluster`, the read before registrations and pool writes, `Ping` (readiness) |
| `Query` | `LoadClusterSet`, `ListClusterSets`, `DeleteCluster` |
| `PutItem` | Cluster registration, export and endpoint set puts, the clusterset index and endpoint partition marker, pool writes inside transactions |
| `UpdateItem` | Cluster and pool heartbeats |
| `DeleteItem` | `DeleteEndpointSet`, pool deletes inside transactions |
| `BatchWriteItem` | `DeleteExport`, `DeleteCluster` |
| `ConditionCheckItem` | The takeover of a stale pool name reservation inside `TransactWriteItems` |

`TransactWriteItems` has no IAM action of its own; DynamoDB authorizes each
item of the transaction with `PutItem`, `UpdateItem`, `DeleteItem` or
`ConditionCheckItem`.

Not granted on purpose: `Scan`, `DescribeTable`, any table or backup
management, the `by_updated_at` index (`.../table/gds/index/*`; add it to
`Query` when an incremental refresh uses it) and DynamoDB Streams (add
`dynamodb:DescribeStream`, `GetRecords`, `GetShardIterator` and
`ListStreams` on `.../table/gds/stream/*` if a change-hint reader is
added).

With a customer managed KMS key, also allow `kms:Decrypt`, `kms:Encrypt`,
`kms:GenerateDataKey*` and `kms:DescribeKey` on the key of each region,
with the condition `"StringEquals": {"kms:ViaService":
"dynamodb.<region>.amazonaws.com"}`. The default AWS owned key needs
nothing.

## Admin role

Used by `gds-admin create-table` and by whoever adds or removes Global Table
replicas. Grant it to operators or a deployment pipeline, not to pods.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "GdsCreateTable",
      "Effect": "Allow",
      "Action": [
        "dynamodb:CreateTable",
        "dynamodb:DescribeTable",
        "dynamodb:DescribeTimeToLive",
        "dynamodb:UpdateTimeToLive",
        "dynamodb:DescribeContinuousBackups",
        "dynamodb:UpdateContinuousBackups",
        "dynamodb:TagResource",
        "dynamodb:ListTagsOfResource"
      ],
      "Resource": [
        "arn:aws:dynamodb:us-east-1:<account>:table/gds",
        "arn:aws:dynamodb:us-west-2:<account>:table/gds"
      ]
    },
    {
      "Sid": "GdsGlobalTableReplicas",
      "Effect": "Allow",
      "Action": [
        "dynamodb:UpdateTable",
        "dynamodb:CreateTableReplica",
        "dynamodb:DeleteTableReplica",
        "dynamodb:Scan",
        "dynamodb:Query",
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:UpdateItem",
        "dynamodb:DeleteItem",
        "dynamodb:BatchWriteItem"
      ],
      "Resource": [
        "arn:aws:dynamodb:us-east-1:<account>:table/gds",
        "arn:aws:dynamodb:us-west-2:<account>:table/gds"
      ]
    },
    {
      "Sid": "GdsReplicationServiceLinkedRole",
      "Effect": "Allow",
      "Action": "iam:CreateServiceLinkedRole",
      "Resource": "arn:aws:iam::<account>:role/aws-service-role/replication.dynamodb.amazonaws.com/AWSServiceRoleForDynamoDBReplication",
      "Condition": {
        "StringEquals": {"iam:AWSServiceName": "replication.dynamodb.amazonaws.com"}
      }
    }
  ]
}
```

- `CreateTable` and the TTL and backup actions are what
  `gds-admin create-table` calls. `TagResource` is needed only when tags
  are passed at creation.
- Adding a replica (`update-table --replica-updates`) requires
  `UpdateTable` and `CreateTableReplica` on the table in the source region
  and the new region, plus the data actions DynamoDB uses to copy the
  existing items, and the replication service-linked role, created once
  per account.
- Deleting the table is deliberately absent; deletion protection is on by
  default. For a teardown, grant `dynamodb:DeleteTable` temporarily and
  turn protection off first with
  `aws dynamodb update-table --no-deletion-protection-enabled`.
- With a customer managed KMS key, the admin role also needs
  `kms:CreateGrant`, `kms:DescribeKey`, `kms:Decrypt` and
  `kms:GenerateDataKey*` on the key of each region.

## IRSA

gds-server runs in an EKS cluster in each GDS region. With IAM roles for
service accounts, the chart annotates the ServiceAccount with the role ARN:

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::<account>:role/gds-server
```

The EKS pod identity webhook injects `AWS_ROLE_ARN`,
`AWS_WEB_IDENTITY_TOKEN_FILE`, `AWS_REGION` and `AWS_DEFAULT_REGION`; the
AWS SDK default credential chain used by the store picks them up.

Trust policy skeleton of the server role. Each EKS cluster has its own OIDC
provider, so there is one statement per cluster (one per region here).
Pin both the audience and the exact ServiceAccount:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "GdsServerUsEast1",
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::<account>:oidc-provider/oidc.eks.us-east-1.amazonaws.com/id/<EAST_OIDC_ID>"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "oidc.eks.us-east-1.amazonaws.com/id/<EAST_OIDC_ID>:aud": "sts.amazonaws.com",
          "oidc.eks.us-east-1.amazonaws.com/id/<EAST_OIDC_ID>:sub": "system:serviceaccount:<namespace>:<serviceaccount>"
        }
      }
    },
    {
      "Sid": "GdsServerUsWest2",
      "Effect": "Allow",
      "Principal": {
        "Federated": "arn:aws:iam::<account>:oidc-provider/oidc.eks.us-west-2.amazonaws.com/id/<WEST_OIDC_ID>"
      },
      "Action": "sts:AssumeRoleWithWebIdentity",
      "Condition": {
        "StringEquals": {
          "oidc.eks.us-west-2.amazonaws.com/id/<WEST_OIDC_ID>:aud": "sts.amazonaws.com",
          "oidc.eks.us-west-2.amazonaws.com/id/<WEST_OIDC_ID>:sub": "system:serviceaccount:<namespace>:<serviceaccount>"
        }
      }
    }
  ]
}
```

- `<namespace>` and `<serviceaccount>` are the release namespace and the
  ServiceAccount name the chart renders.
- Use regional STS endpoints (the default of current SDKs), so token
  exchange does not depend on another region.
- EKS Pod Identity works as well: the trust principal is
  `pods.eks.amazonaws.com` with `sts:AssumeRole` and `sts:TagSession`, and a
  pod identity association replaces the annotation. The permission policy is
  the same.
- A Job that runs `gds-admin create-table` from the chart would need its own
  ServiceAccount bound to the admin role; keep it separate from the server
  ServiceAccount.

## Local development

dynamodb-local accepts any credentials: export `AWS_ACCESS_KEY_ID`,
`AWS_SECRET_ACCESS_KEY` and `AWS_REGION` with dummy values and pass
`--dynamodb-endpoint` (server) or `--endpoint` (gds-admin).
