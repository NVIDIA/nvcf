# Focused upstream OpenBao injector validation

`TestOpenBaoInjectorUpstream` uses an existing isolated cluster and the
selected published OpenBao wrapper. It does not create or delete clusters.
Use a dedicated kubeconfig and an explicit non-production context. Leave
`BDD_CLEANUP_MODE` unset so the harness does not clean unrelated topologies.

Prepare a fresh copy of the self-managed stack from the branch under test.
Merge the authored Manifest page's immutable injector example into its
environment file. Keep the normal published chart selection and custom
OpenBao server/agent images. Configure registry pull Secrets in both
`vault-system` and `injector-workload` without putting credentials in argv.

This focused fixture uses a real OpenBao dev server with ephemeral storage.
In the existing chart values overlay, enable `openbao.server.dev.enabled`,
disable `server.ha.enabled`, `server.standalone.enabled` and
`server.dataStorage.enabled`, and clear `server.extraContainers`, `volumes`
and `volumeMounts`. Enable `server.authDelegator.enabled`. Use a private,
mode-0600 secrets file for the dev root token. Do not use this configuration
for production. The scenario skips wrapper bootstrap/migration hooks because
they require other control-plane services.

Set injector replicas to one and allow the `injector-workload` namespace in
its webhook selector. Install the selected wrapper once to initialize the
fixture. Create the `injector-qa` ServiceAccount in `injector-workload`.
Configure the backend, using its local service-account CA and TokenReview
identity, with:

- A KV v2 mount at `qa` and `qa/injector` containing the non-sensitive fixture
  field `message=upstream-injector-secret-ok`.
- A policy granting `read` on `qa/data/injector`.
- Kubernetes authentication at `auth/kubernetes`, using the cluster API and
  CA. The backend ServiceAccount needs the chart's auth-delegator binding.
- Role `injector-qa`, bound only to ServiceAccount `injector-qa` in namespace
  `injector-workload`, with that policy and `token_period=30s`.

Keep root tokens and registry credentials out of Gherkin and runner logs.
The scenario reads its own short-lived workload token only inside the pod.

Set these environment variables before running from `tests/bdd`:

| Variable | Value |
| --- | --- |
| `KUBECONFIG` | Dedicated isolated-cluster kubeconfig |
| `INJECTOR_CONTEXT` | Explicit context in that file |
| `INJECTOR_STACK_DIR` | Absolute path to the prepared self-managed stack |
| `INJECTOR_ENV` | Prepared environment name |
| `INJECTOR_EXPECTED_IMAGE` | Full upstream `registry/repository:tag@digest` from the authored example |
| `INJECTOR_AGENT_IMAGE` | Existing custom OpenBao agent reference selected by the published wrapper, also used for the test consumer |
| `INJECTOR_PULL_SECRET` | Pull Secret already present in `injector-workload` |

```bash
go test -run '^TestOpenBaoInjectorUpstream$' -count=1 -timeout=10m .
```

The scenario deploys through Helmfile, checks the exact injector reference
and nonroot identity, creates an annotated consumer, checks the unchanged
init/agent references, retrieves the fixture secret and verifies the same
30-second periodic token has renewed after 45 seconds. The consumer uses the
custom OpenBao image for its existing `bao`, `jq` and shell utilities.

The suite leaves the release and test pod for inspection. Before a repeat,
remove only `injected-consumer` in the task namespace, using the explicit
context. Dispose of the isolated fixture through the operator's approved
cleanup process. This does not qualify production JWT/plugin integration,
full-stack HA, upgrade/rollback or an architecture on which it was not run.
