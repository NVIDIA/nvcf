# NVCF 1.1.0 QA candidate

Use `release-nvcf/1.1.0` and its assembled artifact for qualification before
customer publication. [manifest.json](manifest.json) selects Self-Managed
1.1.5 (the control plane), Compute Plane 1.1.0, and Observability 1.0.0 from
their independent source releases. Public NGC catalog publication is deferred.

## Get the selected combination

```sh
git clone --branch release-nvcf/1.1.0 --single-branch \
  https://github.com/dsx-ai-factory/nvcf.git nvcf-1.1.0-qa
cd nvcf-1.1.0-qa
git rev-parse HEAD
jq -r '.stacks | to_entries[] | [.key, .value.version, .value.source_tag] | @tsv' \
  deploy/releases/1.1.0/manifest.json
```

Open the successful `Assemble Umbrella Candidate` workflow for that commit.
Download `umbrella-candidate-<commit>-<run-id>-<attempt>` and extract
`nvcf-1.1.0.tar.gz`. Use the packaged `deploy/stacks/` directories, keeping
all three siblings together. Stack directories in the branch checkout are
not the selected release combination.

The artifact contains the manifest, original released inventories and
`candidate.json`. The assembly summary prints the three source tags. Follow
the extracted stack READMEs for installation inputs. Record the candidate
commit, workflow run and environment with the results in #3182.

## Registry access before installation

Assembly verifies source releases and inventory identities. It does not prove
that every selected chart or image is available in a deployment registry.
Use the QA registry configuration supplied separately by release engineering;
keep credentials and internal registry paths out of this branch.

Before installation, verify access to every required chart and image in the
selected inventories, including migration Jobs and images created by workers.
Use the existing stack registry and repository override settings. Record the
effective references and digests. Keep the selected versions; replacing a
missing version changes the combination being tested. Report missing producer
artifacts to #3182 before running the affected installation or upgrade steps.

Customer catalog checks remain a later publication gate. QA can qualify the
same component versions from engineering registries, with those sources
recorded in its results.

## Qualification scope

- Fresh single- and multi-cluster installs and function, task and LLM BDDs.
- In-place Self-Managed 1.1.2, 1.1.3 or 1.1.4 to 1.1.5 upgrade and repeated apply.
- Upgrade from the preceding 1.0 candidate: Self-Managed 1.0.8, Compute Plane
  1.0.4 and Observability 1.0.0. Follow staged migrations, retain registration
  and workload identities, and verify invocation after each stage.
- Non-root workloads, task storage, ESS secret delivery and certificate
  rotation. Exercise successful and failed LLM streaming, router/Pylon
  communication, and autoscaler scaling and telemetry clients.
- Cassandra and Event Ledger migration completion before dependent services.
  Verify the supported rollback boundary before applying data migrations.
- Observability profiles and retained Kubernetes 1.35 compatibility settings.

Self-Managed 1.1.5 differs from 1.1.4 only in ess-agent 1.4.5 and NATS
auth-callout 0.8.8, the CVE-2026-111943 rebuilds.

Self-Managed 1.1.5 retains ICMS 0.8.5; the later ICMS migration sequence is
excluded. Compute Plane 1.1.0 currently selects NVCA agent/operator 3.18.6.
The proposed NVCA 3.18.9 update is not part of this candidate. If it lands as
a new Compute Plane patch, update the manifest to that released version and
qualify the changed combination.

## After QA

Keep fixes in their owning component and stack maintenance branches. Once a
stack's automatic release completes, consume it by updating this manifest.
Each pin change creates a new candidate artifact. Do not merge the umbrella
branch wholesale into a stack maintenance branch.

After approval of the final combination, complete customer image/chart access
checks and publish the selected stack resources and umbrella. Creating this
branch does not publish a stable umbrella release or activate documentation.
