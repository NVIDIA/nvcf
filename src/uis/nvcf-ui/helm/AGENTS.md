# UI Helm chart

This chart is maintained in this repository. Keep application code changes
under the parent UI subtree. The migration hook uses a different image and
identity from the UI server.

Run from the repository root:

```sh
bash src/uis/nvcf-ui/helm/tests/nonroot-migrations.sh
helm lint src/uis/nvcf-ui/helm
```

Keep the application image version independent from the chart patch version.
Preserve the chart license headers and use the existing Helm conventions.
