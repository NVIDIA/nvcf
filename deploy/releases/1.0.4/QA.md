# NVCF 1.0.4 QA candidate

Use branch `release-nvcf/1.0.4` to qualify this patch before publication.
The stack VERSION files identify candidate versions, not published releases.

| Stack | Candidate version | Source |
| --- | --- | --- |
| Self-Managed | 1.0.2 | #2472 |
| Compute Plane | 1.0.1 | #2738 |
| Observability | 1.0.0 | Released 1.0.0 source |

```sh
git clone --branch release-nvcf/1.0.4 --single-branch https://github.com/NVIDIA/nvcf.git nvcf-1.0.4-qa
cd nvcf-1.0.4-qa
git rev-parse HEAD
```

Record the tested commit and environment with the QA results in #2074.
Use the existing [Self-Managed](../../stacks/self-managed/README.md) and
[Compute Plane](../../stacks/nvcf-compute-plane/README.md) installation
entrypoints. Keep all three stack directories together. Supply explicit test
cluster contexts, environment values, and registry credentials separately.
Engineering registry or chart overrides may be needed until customer
publication completes; record those overrides with the results.

Qualify fresh single- and multi-cluster installation, function/task/LLM BDDs,
non-root workloads, ESS secret delivery and rotation, and the supported
upgrade/rollback path. Upgrade from Self-Managed 1.0.1 and Compute Plane 1.0.0.
Follow the staged upgrade procedure in #2457. Known local API Keys CPU sizing
limitations remain in #2779; record any resource override used by QA.

Send candidate fixes through PRs into this branch and the owning maintenance
branch. Retest affected behavior when the branch changes. After QA approval,
release engineering publishes the approved stack contents and umbrella 1.0.4.
Archive generation and checksum verification are publishing tasks, not QA
entry criteria. Track the remaining release work in #2432.

Do not merge this combined branch wholesale into main or a stack maintenance
branch: it intentionally selects three independent release lines.
