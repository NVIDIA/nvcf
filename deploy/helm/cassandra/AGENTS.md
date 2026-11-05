# Cassandra chart

Native chart in `helm/`, paired with `migrations/cassandra`.
The release service is `helm-nvcf-cassandra`.

Run from the repository root:

```sh
helm lint deploy/helm/cassandra/helm \
  -f tools/ci/helm-validate-values/cassandra.yaml
make -C deploy/helm/cassandra test
```

Keep the selected Cassandra runtime and migration schema compatible with the
maintenance line. Declare image settings in `ownedImages` and test ordinary
values-file overrides. Derive expected defaults from values instead of copying
version literals into tests. Use the existing Helm template style and ASCII text.
