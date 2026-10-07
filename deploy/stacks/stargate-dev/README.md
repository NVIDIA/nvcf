# Stargate development deployment (example)

This directory is an example of a multi-region Stargate development
environment on AWS. It runs Stargate against mock inference backends
(MockDynamo with a Pylon sidecar) so routing policies can be benchmarked
across regions without GPUs.

It is not a supported production deployment. Every AWS account ID, ARN,
hostname, image reference, and network range checked in here is a
placeholder. A deployment supplies its own values in a protected file outside
the repository.

## What it deploys

For each region in `environments/`:

- One hub EKS cluster with Stargate, the backend router, the dev-only auth
  fixture, and a Grafana Alloy metrics collector.
- Two MockDC EKS clusters. Each runs two backends, and each backend is one
  MockDynamo container with a Pylon sidecar.

The example environments define five regions (`us-west-2`, `us-east-1`,
`eu-west-1`, `ap-northeast-1`, `ap-southeast-2`) with 20 backends in total.
Every Pylon registers with every regional Stargate.

## AWS resources you provide

- An AWS account. Its ID is read from the environment variable named by
  `awsAccountIdEnv` (`STARGATE_DEV_AWS_ACCOUNT_ID` in the examples).
- Per region, three EKS clusters whose names, kube contexts, Kubernetes
  version, node type, and node count match `environments/<region>.yaml`.
  Deployment preflight checks each value and requires cluster-admin access.
- The AWS Load Balancer Controller on each hub cluster. The registration
  endpoint is a network load balancer with TLS on port 50071 and UDP on 50072.
- An ACM certificate and a DNS name for that endpoint, and the NAT egress
  ranges of the MockDC clusters.
- A container registry with Stargate, dev auth, MockDynamo, and Pylon images,
  pinned by digest.
- Optional observability: an Amazon Managed Service for Prometheus workspace,
  an IAM role for the Alloy writer, and an IAM role for Grafana.
  `scripts/observability.py` can create them.

## Placeholders

Replace these in your protected values file. Do not commit real values.

| Setting | Example placeholder |
|---|---|
| `router.hostname` | `router.usw2.stargate-dev.example.invalid` |
| `router.acmCertificateArn` | `arn:aws:acm:us-west-2:000000000000:certificate/00000000-...` |
| `router.loadBalancerSourceRanges` | `192.0.2.1/32` (documentation range) |
| `observability.amp.workspaceId` | `ws-00000000-0000-0000-0000-000000000000` |
| `observability.amp.writerRoleArn` | `arn:aws:iam::000000000000:role/stargate-dev-amp-writer` |
| `observability.grafana.readerRoleArn` | `arn:aws:iam::000000000000:role/stargate-dev-grafana` |
| `images.*` in `values/versions.yaml` | `example.invalid/<image>` with an all-zero digest |

`deploymentReady` stays `false` in the checked-in files, and the apply wrapper
refuses to deploy until the protected values set it to `true`.

## Deploy

Run from this directory with `helm`, `helmfile`, `kubectl`, and the AWS CLI on
`PATH`, and with credentials for your account.

1. Create the protected credential bundle for a region:

   ```sh
   python3 scripts/deploy.py init --region us-west-2 --credentials /secure/path/us-west-2/credentials.json
   ```

2. Write a protected values file for the region with your hostnames, ARNs,
   image digests, and `deploymentReady: true`.

3. Apply each phase:

   ```sh
   export STARGATE_DEV_AWS_ACCOUNT_ID=<your account ID>
   python3 scripts/deploy.py apply --region us-west-2 --phase stargate --credentials /secure/path/us-west-2/credentials.json --values /secure/path/us-west-2/values.yaml
   python3 scripts/deploy.py apply --region us-west-2 --phase mockdc --credentials /secure/path/us-west-2/credentials.json --values /secure/path/us-west-2/values.yaml
   ```

4. Optionally provision observability with
   `python3 scripts/observability.py --region us-west-2 --values /secure/path/us-west-2/values.yaml`,
   then apply the `observability` phase.

## Layout

- `environments/`: one file per region with cluster names, node shapes, and
  per-backend MockDynamo settings.
- `values/versions.yaml`: image and chart version pins.
- `helmfile.yaml.gotmpl`: releases and install order.
- `charts/`: the dev auth fixture and MockDC charts.
- `scripts/`: deployment and observability wrappers.
- `benchmark/`: the `stargate-dev-bench` controller for verification and
  benchmark campaigns.
- `dashboards/`, `loadtest/`: Grafana dashboards and load profiles.
- `reports/`: published benchmark reports.
- `tests/`: rendering and invariant tests.

## Tests

```sh
python3 -m unittest discover -s tests
```

The rendering tests need `helm` and `helmfile` and use only the checked-in
placeholders.

See `PLAN.md` for the design, the benchmark workflow, and acceptance steps.
