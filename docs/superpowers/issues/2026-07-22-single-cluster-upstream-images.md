# Single-Cluster Upstream Images Issue Log

## Design self-review

### BDD-UPSTREAM-001: Local source worktree has no stack Makefile

Status: fixed in design

`deploy/stacks/self-managed/Makefile` is not tracked. The existing BDD command
shape expects it, while the repository contains `Makefile.dist`. The new
feature will copy `Makefile.dist` to `Makefile` through the file-operation
ledger, making the live test self-contained and ensuring teardown removes the
generated file.

### BDD-UPSTREAM-002: Initial scenario split repeated exact substitutions

Status: fixed in design

The first design used separate render and install scenarios. Re-running the
same Background would apply exact substitutions more than once and could hide
documentation drift. The feature now uses one end-to-end scenario, so every
documented replacement is applied exactly once.

### BDD-UPSTREAM-003: New substitution step lacked catalog sequencing

Status: fixed in design

`tests/bdd/AGENTS.md` requires a new step to land in `PLAN.md` before handler
code and to have a positive unit test. The implementation plan now orders the
catalog update, failing step test, thin handler, and passing tests explicitly.

### BDD-UPSTREAM-004: Cleanup verification matrix names an insufficient mode

Status: fixed

`PLAN_DESTRUCTIVE_CLEANUP.md` says `topology-single` clears a stale
multi-cluster topology before a single-cluster run. The implementation maps
that mode only to deletion of `ncp-local`; it does not delete
`ncp-local-cp` or compute clusters. The live run will use
`BDD_CLEANUP_MODE=topology-multi`, which deletes every `ncp-local*` cluster.
The verification matrix now uses that implemented mode for the
multi-cluster-to-single-cluster transition.

### BDD-UPSTREAM-005: Quoted substitution cannot express YAML blocks

Status: fixed in design

The initially approved quoted substitution step could replace a unique
repository line but could not safely replace the repeated registry line next
to it. The step now accepts a docstring containing an old and new multi-line
block separated by one `---` line. A pure DSL helper validates the shape and
fails if the old block is absent.

### BDD-UPSTREAM-006: Helm server-side dry-run needs the local cluster

Status: fixed in design

The stack Helmfile defaults include `--dry-run=server`. The initial design
placed `make template` before cluster bootstrap. The revised scenario creates
the single-cluster topology and pull secrets before rendering.

## Implementation and live-run issues

### BDD-UPSTREAM-007: Cluster bootstrap requires a Docker config mount

Status: fixed

The first destructive live run failed in
`make -C tools/ncp-local-cluster build-and-deploy-cluster` because
`tools/ncp-local-cluster/secrets/docker-config.json` was absent. The k3d
configuration mounts that file into the kubelet credential provider.

The first attempted fix generated this file through the suite ledger. A later
run showed that was unsafe because the file is a live Docker bind-mount source.
The final fix treats it as the durable prerequisite already required by the
cluster Makefile and asserts its presence before bootstrap.

### BDD-UPSTREAM-008: Helm did not use the generated NGC credentials

Status: fixed

The second destructive live run built the k3d topology but `make template`
failed with `403 Access Denied` while Helm pulled the private cert-manager
chart. The generated Docker config was mounted into the kubelet but Helm still
read its default registry configuration.

The feature now sets `HELM_REGISTRY_CONFIG` to the absolute path of the same
gitignored config on both the template and install commands. Only the config
path appears in command logs; the credential remains in the gitignored file.

### BDD-UPSTREAM-009: Ledger removed a live Docker bind-mount source

Status: fixed

After the second run, suite teardown removed the generated Docker config while
the k3d cluster still referenced it. During the next destructive cleanup,
Docker recreated the missing source path as a directory, and the feature
failed before bootstrap with `docker-config.json: is a directory`.

The feature no longer authors or restores that file. It is now an explicit,
durable local prerequisite. The wiring test seeds a fake config only inside
its temporary repository.

### BDD-UPSTREAM-010: Existing Docker config contained rejected NGC auth

Status: fixed locally and documented

The fourth destructive run proved Helm read the configured registry file, but
NGC still returned `403 Access Denied`. The copied Docker config had an
`nvcr.io` auth entry, but that stored credential was rejected for the sample
chart repository.

The local config was refreshed with `helm registry login` using
`NGC_API_KEY` through password-stdin. The README now includes that non-echoing
login command and states that the key needs pull access to the configured
sample NGC org and team.

### BDD-UPSTREAM-011: Two documented Alpine overrides do not own workloads

Status: fixed in documentation and captured by the feature

The fifth destructive run rendered the stack successfully and exposed two
documentation-to-chart mismatches. The current Cassandra initialization hook
uses `nvcf-cassandra-migrations`; the legacy
`cassandra.initialization.image` value does not control that workload. The
current NATS chart renders NKeys directly as Secrets and has no nkey job for
`nats.nkeyJob.image` to control.

The development and `0.6.1-rc` docs now recommend only the supported API
account-bootstrap Alpine override and explain that the Cassandra and NATS
operations require chart support. The feature asserts the supported NATS
reloader and API images while explicitly checking the current Cassandra and
NATS render behavior.

### BDD-UPSTREAM-012: Full-stack install is not portable to local arm64

Status: fixed by focusing the feature on owning releases

The sixth destructive run proved the NATS reloader and API account-bootstrap
hook both pulled and ran successfully, but a later, unrelated
`nvcf-invocation-service:0.8.5` container exited immediately with code 133 on
the arm64 Docker host. Helmfile consequently waited for that release until its
15-minute timeout even though the upstream-image behavior under test had
already passed.

The feature now installs `release-group=dependencies`, followed by the API's
`ess-api` prerequisite and `api` itself. This is still a real single-cluster
Helmfile bring-up: it installs the NATS StatefulSet and executes the API
account-bootstrap hook. It excludes unrelated services whose image
architecture is not part of this test. Each selector was first verified
against the live cluster before the feature was changed.

### BDD-UPSTREAM-013: Process interruption bypasses ledger teardown

Status: recovered; fail-loud behavior verified

The seventh run stopped immediately because the previous full-stack run had
been interrupted with SIGINT while Helmfile was waiting. Process termination
bypassed the suite teardown, so the two ledger-owned substitutions remained
in `global.yaml.gotmpl`. The exact block replacement then correctly reported
that its old block was absent instead of silently continuing.

The two known test-owned changes were restored exactly from the Git diff
before retrying. Normal success and scenario-failure paths remain
ledger-restored; operators should avoid process-level interruption during a
file-mutating live feature, or restore the reported diff before retrying.

### BDD-UPSTREAM-014: API has an undeclared NATS auth runtime prerequisite

Status: fixed

On the eighth clean-cluster run, the dependency group and ESS installs passed
and the public `alpine/k8s` account-bootstrap hook completed. The API
Deployment then failed to connect to NATS because
`nats-auth-callout-service` was not installed. The API Helmfile release
declares only `ess-api` in `needs`, so a selector-focused install does not
discover this runtime dependency.

Installing `name=nats-auth-callout-service` caused the existing API pod to
recover and the feature to pass all 42 steps. The feature now installs that
selector before `name=api` and asserts its Helm release, making the dependency
part of the self-contained test rather than a diagnostic intervention.

### BDD-UPSTREAM-015: Cassandra behavior assertion overfit an image tag

Status: fixed in self-review

The initial limitation check asserted
`nvcf-cassandra-migrations:0.11.0`. The documentation claim is about which
image value owns the workload, not the current migrations version, so a normal
tag update would have broken this feature without changing the behavior under
test.

The render assertion now matches `nvcf-cassandra-migrations:` and remains
scoped to the Cassandra release output.
