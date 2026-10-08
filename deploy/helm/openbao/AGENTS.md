# OpenBao deployment and chart

This subtree is maintained in this repository. The chart mounts scripts from
`helm/scripts/` into its initialization Job. Keep shared prerequisite behavior
consistent with the standalone `deploy.sh`.

Run focused checks from the repository root:

```sh
bash deploy/helm/openbao/tests/init-tool-requirements.sh
bash deploy/helm/openbao/tests/auto-unseal-sidecar.sh
bash deploy/helm/openbao/tests/plugin-catalog-refresh-hook.sh
```

The chart render test requires Helm and yq. Build chart dependencies with
`make -C deploy/helm/openbao deps`. Live deployment needs an explicit test
cluster and registry credentials; follow the root QA environment rules.

Use Bash and match existing script conventions. Test both standalone `script`
mode and in-cluster `helm` mode when changing initialization prerequisites.
The latter must work without a Helm executable in the migration image.
