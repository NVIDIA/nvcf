# Chart image values QA

`TestChartImageValues` applies ordinary values files with Helm in an isolated
QA cluster and inspects Pod images. It does not invoke inventory or image
mirror tooling. The default chart settings and all indirect image overrides
are checked separately by `TestImageCatalogCharts` in the inventory module.

Follow root QA rules before preparing a cluster. The suite upgrades four QA
releases in the `catalog-nats`, `catalog-cert-manager`, `catalog-openbao` and
`catalog-cassandra` namespaces. It uses `--no-hooks` to exercise image wiring
without rerunning bootstrap or data migrations. It does not qualify upgrades
of production data, pending security images or complete migration lifecycles.

Prerequisites:

- Helm, kubectl and copies of the current charts with dependencies built.
- A QA directory containing a dedicated `kubeconfig` and ordinary
  `<profile>-source-values.yaml` and `<profile>-mirror-values.yaml` files for
  `nats`, `cert-manager`, `openbao` and `cassandra`.
- Available images in the selected registry, with credentials and cluster
  trust configured. Establish artifact identity during release preparation.

Run from the repository root:

```sh
IMAGE_VALUES_CHART_ROOT=/absolute/path/current-qa-charts \
IMAGE_VALUES_QA_ROOT=/absolute/path/isolated-qa \
IMAGE_VALUES_MIRROR_PREFIX=registry.example.com/renamed/ \
go test -C tests/bdd -run '^TestChartImageValues$' -count=1 -v
```

Keep the anchored test name. `TestChartImageValuesWiring` uses fake command
results and does not validate a deployment. Keep live installation evidence
separate from fixture checks and release-publication evidence.
