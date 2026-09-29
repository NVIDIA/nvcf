# AGENTS.md - pylon-operator

Kubernetes operator that publishes models served in the cluster to the LLM
invocation plane. It reconciles `InferenceEndpoint`
(`pylon.nvidia.com/v1alpha1`): it probes the backend Service, resolves the GPU
type from node labels, runs one Pylon transport Deployment per endpoint and
reports status. It is a new service and shares no
code with NVCA.

## Layout

- `cmd/pylon-operator/`: entrypoint, flags and manager setup
- `api/v1alpha1/`: CRD types, condition types and typed reasons
- `config/crd/bases/`, `config/rbac/`: generated CRD and ClusterRole
- `internal/controller/`: reconciler, steps, watches and field indexes
- `internal/prober/`: the `Ready` condition (Service, endpoints, health, model listing)
- `internal/gpu/`: `status.gpu` from `spec.gpu.product` or `nvidia.com/gpu.product` node labels
- `internal/transport/`: the transport Deployment (`pylon-<name>`, owned by the
  endpoint) and replication of the cluster credential Secret and trust bundle
  ConfigMap from the operator namespace into the endpoint's namespace
- `internal/registration/`: the registration observer (`TransportReady`,
  `Registered`, `status.registration`, `status.servers`) from the transport
  pods' Pylon metrics
- `internal/metrics/`: Prometheus collectors
- `internal/config/`: flags, validation and manager options
- `internal/version/`: build metadata set by Bazel `x_defs`
- `internal/integration/`: envtest suite against a real API server
- `hack/`: codegen script and Go license header

## Build and test

From this directory:

```bash
make build            # _output/bin/pylon-operator
make test             # unit tests with -race and coverage
make test-envtest     # envtest suite; downloads kube-apiserver and etcd once
make lint             # golangci-lint with .golangci.yml
make vet              # go vet and gofmt
make codegen-update   # regenerate deepcopy, CRD and RBAC
make codegen-check    # fail when generated files are stale
```

The Makefile sets `GOWORK=off` so `go` resolves against this module's `go.mod`.

From the repository root:

```bash
bazel test //src/compute-plane-services/pylon-operator/... --test_output=errors
bazel build //src/compute-plane-services/pylon-operator:image_index
bazel run //:gazelle -- src/compute-plane-services/pylon-operator
bazel mod tidy        # after adding an external import
```

The envtest target `//src/compute-plane-services/pylon-operator/internal/integration:integration_test`
is tagged `external` and `requires-kubebuilder`, inherits `KUBEBUILDER_ASSETS`,
and skips when it is unset. To run it under Bazel:

```bash
KUBEBUILDER_ASSETS="$(make -s -C src/compute-plane-services/pylon-operator envtest-assets)" \
  bazel test //src/compute-plane-services/pylon-operator/internal/integration:integration_test
```

Unit test coverage of `internal/` is expected to stay at or above 85 percent.

To build every image and run the operator with the LLM gateway stack on k3d,
follow `docs/dev/inference-endpoints-quickstart.md`.

## Code generation

Edit the types or kubebuilder markers in `api/v1alpha1/` or the RBAC markers in
`internal/controller/reconciler.go`, then run `make codegen-update` and commit
the generated files. Never edit `zz_generated.deepcopy.go`, `config/crd/bases/`
or `config/rbac/role.yaml` by hand. `hack/codegen.sh` pins controller-tools
v0.19.0, which matches the Kubernetes 1.34 libraries in `go.mod`.

## Library policy

By user decision this service uses controller-runtime's own libraries and does
not depend on `src/libraries/go/lib`. This overrides the root rule to log with
`logrus` for this service.

- Logging: `ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))` in `main`, and
  `log.FromContext(ctx)` everywhere else. Zap flags come from
  `zap.Options.BindFlags`.
- Metrics: register on `sigs.k8s.io/controller-runtime/pkg/metrics.Registry`;
  the manager's metrics server serves them on `--metrics-bind-address`.
- Health: `mgr.AddHealthzCheck` and `mgr.AddReadyzCheck` on
  `--health-probe-bind-address`.
- Leader election and namespace-scoped caches: `manager.Options`, built in
  `internal/config`.
- Events: `mgr.GetEventRecorderFor("pylon-operator")`.
- Flags: the standard `flag` package.

## Dependency versions

Bazel resolves one version of each Go module across `go.work.bazel`. Keep
`sigs.k8s.io/controller-runtime` at the version Bazel resolves and `k8s.io/*`
at the matching minor. Check with:

```bash
bazel mod show_repo @io_k8s_sigs_controller_runtime @io_k8s_api
```

`src/libraries/go/lib` pins `k8s.io/api`, `k8s.io/apimachinery` and
`k8s.io/client-go` with replace directives, and the first replace in
`go.work.bazel` wins. Bazel therefore builds those three at the lib's version
while plain `go` uses the patch level in this `go.mod`. Do not add replace
directives here.

## Conventions

- Metric names follow `nvcf_pylon_operator_<name>_<unit>` and every label
  combination is pre-initialised in `internal/metrics.New`.
- Conditions use the typed reasons in `api/v1alpha1/conditions.go` through
  `ReconcileContext.SetReady`, `SetTransportReady` and `SetRegistered`.
- A reconcile runs the ordered steps of `controller.DefaultSteps` on one
  `ReconcileContext` and patches status once, with an optimistic lock. Each
  step writes only the status fields it owns and queues Events with
  `rc.Event`; Events are emitted after the patch succeeds.
- New behaviour is a new step. A step that needs watches or indexes implements
  `controller.ManagerSetup`.
- `status.gpu` never affects conditions or the transport Deployment: Pylon is
  not given the GPU type, so a GPU type change does not roll the transport.
- The transport step records `ReconcileContext.Transport` and sets the fast
  negatives of `TransportReady` and `Registered` (`ScaledToZero`, then
  `TransportPodsNotRunning`). When `rc.TransportFastNegative()` reports one,
  later steps leave both conditions alone; otherwise the registration observer
  owns them.
- Steps that keep per-endpoint state in memory implement
  `controller.Forgetter`; the reconciler calls `Forget` when the endpoint is
  gone.
- The transport template is compared by the `pylon.nvidia.com/transport-spec-hash`
  pod-template annotation only. Any change to rendering rolls every transport
  Deployment once.
- Never log, wrap into an error or put into an Event the content of the
  credential Secret or trust bundle ConfigMap.
- The operator's own caches: Deployments and Pods labelled
  `app.kubernetes.io/managed-by=pylon-operator`, and only the Secrets and
  ConfigMaps with the configured names (`config.ManagerOptions`).
- The leader election Role (Leases in the operator namespace) is not generated;
  the chart provides it.

## Registration observer

The last step of `DefaultSteps`. On a transport fast negative it does not
scrape: it sets `status.registration.routersConnected` to 0, keeps
`lastRegisteredTime` and clears `status.servers`. Otherwise it lists the
transport pods by `transport.State.PodLabels`, GETs
`http://<podIP>:9089/metrics` on every running pod with an IP, concurrently
with a 2 second timeout, and requeues after `--scrape-interval` plus or minus
10 percent. A pod that cannot be scraped counts as no streams and no tunnels
(logged at V(1), counted in `nvcf_pylon_operator_scrape_failures_total`).

It reads three Pylon series, pinned by a contract test in Pylon's CI; renamed
series are not tolerated:

- `pylon_registration_stream_connected{router}`: 1 while the registration
  stream to that router is open
- `pylon_reverse_tunnel_connected{router}`: 1 while the QUIC tunnel is up
- `pylon_registration_stream_closures_total{router,reason}`: a rise of
  `unauthenticated` or `invalid_argument` is a rejection

Reasons, in order:

- `Registered`: True `RegisteredWithRouter` when any pod has a stream to any
  router, also while `Ready` is False (registered, unhealthy); else
  `WaitingForUpstream` while `Ready` is not True; else `RegistrationRejected`
  for 60 s after a rejection; else `RouterUnreachable` once the first pod has
  been ready for more than 30 s; else `Pending`.
- `TransportReady`: True `PylonConnected` when the Deployment has ready pods
  and any pod has a tunnel; else `WaitingForUpstream` while `Ready` is not
  True; else `TunnelNotConnected`, whose message counts the pods with a stream
  but no tunnel.

Closure counters are compared per pod, router and reason with the previous
successful scrape. A decrease means Pylon restarted, so the baseline is zero
again. At first sight a pod that started before the observer (an operator
restart) sets the baseline; a newer pod counts from zero. Each new rejection
also emits a `RegistrationStreamRejected` Warning, which names the pod, router
and reason even while another router keeps `Registered` True.
`lastRegisteredTime` is set when `Registered` becomes True and refreshed once
a minute while it stays True. `nvcf_pylon_operator_registered{endpoint}` and
`nvcf_pylon_operator_time_to_registered_seconds` (creation to first
registration, once per endpoint) complete the picture.

CI subproject id: `pylon-operator`.
