# NVCF 1.0.4 QA candidate

Use branch `release-nvcf/1.0.4` and its assembled candidate to qualify this patch
before customer publication. [manifest.json](manifest.json) selects three
independently released stacks. The candidate remains unqualified until this
complete combination passes QA.

| Stack | Selected version | Source tag |
| --- | --- | --- |
| Self-Managed | 1.0.7 | `deploy/stacks/self-managed/v1.0.7` |
| Compute Plane | 1.0.4 | `deploy/stacks/nvcf-compute-plane/v1.0.4` |
| Observability | 1.0.0 | `deploy/stacks/observability/v1.0.0` |

```sh
git clone --branch release-nvcf/1.0.4 --single-branch https://github.com/NVIDIA/nvcf.git nvcf-1.0.4-qa
cd nvcf-1.0.4-qa
git rev-parse HEAD
```

Open the successful `Assemble Umbrella Candidate` workflow run for the recorded
commit. Download its `umbrella-candidate-<commit>-<run-id>-<attempt>` artifact,
then extract `nvcf-1.0.4.tar.gz`. The artifact includes `candidate.json` and the
original released inventories so the tested combination is identifiable.

Use the installation entrypoints and README files in the extracted stack
directories. Keep all three sibling stack directories together. The assembled
bundle takes each stack from its selected tag; incidental stack directories in
the QA branch checkout are not the selected candidate. Supply explicit test
cluster contexts, environment values, and registry credentials separately.
Use the selected published images and charts. Record any required override
with the results rather than treating it as validation of the default pins.

Record the candidate commit, workflow run, environment, and results in #2074.
The comparison baseline is documentation edition 1.0.2: Self-Managed 1.0.1,
Compute Plane 1.0.0, and Observability 1.0.0. The earlier QA branch selected
Self-Managed 1.0.2 and Compute Plane 1.0.1; that earlier approval does not qualify
this updated combination. The complete component and chart delta is tracked
in #2823.

Qualify fresh single- and multi-cluster installation, function/task/LLM BDDs,
non-root workloads, ESS secret delivery and rotation, and the supported
upgrade/rollback path. Upgrade from Self-Managed 1.0.1 and Compute Plane 1.0.0.
Follow the staged upgrade procedure in #2457. Include the updated NICLLS,
Samba, and collector paths. Samba 1.0.9 fixes startup ownership of the shared
task directory. Verify that non-root Helm tasks create their results directory
and finish successfully. Record any resource overrides used by QA.

Land stack fixes in their owning maintenance branches and let their automatic
releases complete. Then update this branch's manifest to consume the selected
released versions. Candidate CI validates their inventories and assembles the
combination for retesting. It does not publish a customer release.

After approval of the final candidate, release engineering publishes the three
selected stack resources and umbrella 1.0.4. Keep that publication on hold
until QA completes. Track the selected combination in #2492 and remaining
release work in #2432. Later CVEs, state-metrics changes, heavier Cassandra and
OpenBao upgrades, and documentation site activation remain deferred.

Do not merge this combined branch wholesale into main or a stack maintenance
branch: it intentionally selects three independent release lines.
