# NVCA Release Process

NVCA (NVIDIA Cloud Functions Agent) uses an automated CI/CD pipeline to build,
test, and publish releases. This document covers release tagging,
versioning conventions, and the downstream staging and production steps that
follow a tag.

## Prerequisites

1. Set the `GITLAB_TOKEN` environment variable with a GitLab token that has API
   access for pushing tags.
2. Ensure you have push access to the repository and are on the correct branch
   for the type of release being performed.

## Release Tagging

Use `git tag` locally to create a tag for a release, then push it remotely.

### Major and Minor Tags

Major and minor releases are made from the `main` branch. Release candidate tags
are created before cutting a major or minor release to validate the final build.
RC tags have the format `vX.Y.Z-rc.N`, where `N` is an integer starting at 0.

```bash
git checkout main
git pull
git tag v1.20.0-rc.0
git push origin tag v1.20.0-rc.0
```

Once a release candidate has been validated by QA, create the final release tag
from the validated RC tag.

```bash
# Assume rc.5 is the validated tag.
git checkout v1.20.0-rc.5
git tag v1.20.0
git push origin tag v1.20.0
```

### Patch Tags

Patch tags must be made from a release branch with the pattern `release-X.Y`.

```bash
git checkout release-1.20
git pull
git tag v1.20.1
git push origin tag v1.20.1
```

### Dev Tags

Dev tags can be created from any branch for testing purposes. They use the
format `vX.Y.Z-dev.N`.

```bash
git tag v1.20.0-dev.0
git push origin tag v1.20.0-dev.0
```

## Versioning

This project uses [Semantic Versioning](https://semver.org/) with a `v` prefix
for git tags.

| Format | Description | Example | Audience |
|--------|-------------|---------|----------|
| `vMAJOR.MINOR.PATCH` | Release version | `v1.20.0` | QA / Production |
| `vMAJOR.MINOR.PATCH-dev.N` | Dev/prerelease build | `v1.20.0-dev.0` | Dev |
| `vMAJOR.MINOR.PATCH-rc.N` | Release candidate (stage) | `v1.20.0-rc.1` | QA |

Version precedence, from lowest to highest:

- `v1.20.0-dev.0`
- `v1.20.0-dev.1`
- `v1.20.0-rc.1`
- `v1.20.0`

## Tag Pipeline

Tags trigger the `.rule-tagged` jobs in GitLab CI. Version information is
derived via `nv_ci_versioning` and stored in `EGX_VERSION`.

The tag is built via the automated
[pipeline](https://github.com/NVIDIA/egx/intelligent-infra/nvca/-/pipelines?scope=tags&page=1).
Once the `Release-Artifacts` stage has passed, run the `gitlab-release` step in
the tag pipeline.

## Staging

### 1. Publish images and charts

Create a new config in the [`ngc/publishing/ngc-publishing-configs`](https://github.com/NVIDIA/ngc/publishing/ngc-publishing-configs)
repo under [`/configs/amiryala`](https://github.com/NVIDIA/ngc/publishing/ngc-publishing-configs/-/tree/main/configs/amiryala)
by copying a prior config and substituting in the appropriate NVCA and NVCA Operator image and chart versions.
Omit NVCA Operator chart and image from the config if they are not being upgraded.

For example, a stg release with both NVCA and NVCA Operator version bumps:

```yaml
nspect_id: "NSPECT-S7AK-H2SE"
vpr_doc_url: "https://docs.google.com/document/d/1mjdTBSCp1-0EpxH2Gjm5Bto0KS41DJHeEv9FVs9VMmM/edit#heading=h.owtpcsbhn680"
support_doc_url: "https://docs.google.com/document/d/11P1oGrGxMqAo_mN3C9a7e3ffHOhY5AHSliUrpJ4vNNo/edit"
qa_report_url: "http://hqswqadb02/devtestreportsv2/CustomRpt/CoverageReport.aspx?reportId=46764"

source:
  org: "nvidia"
  team: "byoc"

target:
  org: "nvidia"
  team: "nvcf-byoc"

artifacts:
  - source_name: "nvca"
    source_version: "2.37.0"
    type: "container"
  - source_name: "nvca-webhook-server"
    source_version: "2.37.0"
    type: "container"
  - source_name: "nvca-operator"
    source_version: "1.6.0"
    type: "chart"
  - source_name: "nvca-operator"
    source_version: "1.6.0"
    type: "container"

options:
  public: true
  guest_access: false
  searchable: true

environment: "staging"
```

Make sure to tag a team member on the MR. Request a review on the MR in [#ngc-publishing-git-pipeline](https://nvidia.enterprise.slack.com/archives/C07D8L7EN6M). Example:
```
@ngc-publishing please review the nvca 2.45.10 stg release MR, thanks! https://github.com/NVIDIA/ngc/publishing/ngc-publishing-configs/-/merge_requests/1837
```

Usually the MR will be reviewed within a few hours.

### 2. Add NVCA and NVCA Operator versions to ICMS config

Add a new JSON object to the [`nvcf-deployment-qat.yaml`](https://github.com/NVIDIA/nvcf/deployment-configs/deployment-config/-/blob/master/nvcf-deployment-qat.yaml)
file in the repo [`nvcf/deployment-configs/deployment-config`](https://github.com/NVIDIA/nvcf/deployment-configs/deployment-config/-/blob/master/)
containing NVCA and NVCA Operator versions, and all capabilities that version should have available.
Note that NVCA Operator version does not have to change from the previous object's version,
but NVCA's version does.

Make sure to tag a team member on the MR.

For example, a stg release with both NVCA and NVCA Operator version bumps:

```diff
nvca:
  ...
  cluster-version: |
    {
      "clusterKeyExpirationDays": 90,
      "computePlatforms": [
          "AWS",
          "AZURE",
          "OCI",
          "ON-PREM",
          "GCP",
          "DGX-CLOUD"
      ],
      "attributes": [
          "KataRuntimeIsolation",
          "HostIsolation"
      ],
      "nvcaVersions": [
+         {
+           "version": "2.37.0",
+           "capabilities": [
+             "DynamicGPUDiscovery",
+             "CachingSupport",
+             "LogPosting",
+             "HelmResourceConstraints",
+             "BinPackTenantWorkloads"
+           ],
+           "operatorVersion": "1.6.0"
+         },
          {
            "version": "2.36.2",
            "capabilities": [
              "DynamicGPUDiscovery",
              "CachingSupport",
              "LogPosting"
            ],
            "operatorVersion": "1.5.0"
          },
          ...
```

For a patch version, it's acceptable to update the most recent version to the patch instead of adding a new entry.

## Production

### 1. Publish images and charts

Create a new config in the [`ngc/publishing/ngc-publishing-configs`](https://github.com/NVIDIA/ngc/publishing/ngc-publishing-configs)
repo under [`/configs/nvcf-byoc`](https://github.com/NVIDIA/ngc/publishing/ngc-publishing-configs/-/tree/main/configs/nvcf-byoc)
by copying a prior config and substituting in the appropriate NVCA and NVCA Operator image and chart versions.
Omit NVCA Operator chart and image from the config if they are not being upgraded.

Make sure to tag a team member on the MR.

For example, a prod release with both NVCA and NVCA Operator version bumps:

```yaml
nspect_id: "NSPECT-S7AK-H2SE"
vpr_doc_url: "https://docs.google.com/document/d/1mjdTBSCp1-0EpxH2Gjm5Bto0KS41DJHeEv9FVs9VMmM/edit#heading=h.owtpcsbhn680"
support_doc_url: "https://docs.google.com/document/d/11P1oGrGxMqAo_mN3C9a7e3ffHOhY5AHSliUrpJ4vNNo/edit"
qa_report_url: "http://hqswqadb02/devtestreportsv2/CustomRpt/CoverageReport.aspx?reportId=46764"

source:
  org: "nvstaging"
  team: "nvcf-byoc"

target:
  org: "nvidia"
  team: "nvcf-byoc"

artifacts:
  - source_name: "nvca"
    source_version: "2.34.3"
    type: "container"
  - source_name: "nvca-webhook-server"
    source_version: "2.34.3"
    type: "container"
  - source_name: "nvca-operator"
    source_version: "1.6.0"
    type: "chart"
  - source_name: "nvca-operator"
    source_version: "1.6.0"
    type: "container"

options:
  public: true
  guest_access: false
  searchable: true
```

### 2. Add NVCA and NVCA Operator versions to ICMS config

Add a new JSON object to the [`nvcf-deployment.yaml`](https://github.com/NVIDIA/nvcf/deployment-configs/deployment-config/-/blob/master/nvcf-deployment.yaml)
file in the repo [`nvcf/deployment-configs/deployment-config`](https://github.com/NVIDIA/nvcf/deployment-configs/deployment-config/-/blob/master/)
containing NVCA and NVCA Operator versions, and all capabilities that version should have available.
Note that NVCA Operator version does not have to change from the previous object's version,
but NVCA's version does.

Make sure to tag a team member on the MR.

For example, a prod release with both NVCA and NVCA Operator version bumps:

```diff
nvca:
  ...
  cluster-version: |
    {
      "clusterKeyExpirationDays": 90,
      "computePlatforms": [
          "AWS",
          "AZURE",
          "OCI",
          "ON-PREM",
          "GCP",
          "DGX-CLOUD"
      ],
      "attributes": [
          "KataRuntimeIsolation",
          "HostIsolation"
      ],
      "nvcaVersions": [
+         {
+           "version": "2.37.0",
+           "capabilities": [
+             "DynamicGPUDiscovery",
+             "CachingSupport",
+             "LogPosting",
+             "HelmResourceConstraints",
+             "BinPackTenantWorkloads"
+           ],
+           "operatorVersion": "1.6.0"
+         },
          {
            "version": "2.36.2",
            "capabilities": [
              "DynamicGPUDiscovery",
              "CachingSupport",
              "LogPosting"
            ],
            "operatorVersion": "1.5.0"
          },
```

### 3. Inform NVCF SRE of new version via maintenance ticket

Follow [this guide](https://confluence.nvidia.com/display/PLATFORM/How+to+file+an+NVCFSRE+Maintenance+ticket)
so NVCF SRE knows to upgrade clusters to the latest production image.
