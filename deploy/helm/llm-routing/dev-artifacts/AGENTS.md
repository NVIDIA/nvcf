# Temporary development artifacts

Keep image build/preload helpers, their import and preparation charts, image values, the package script and generated distribution under this directory. Permanent shared/model chart sources and recipe catalogs stay outside it. Runtime and CLI modules must work without importing this directory.

`build.py` prepares images, preloads selected nodes, packages charts and publishes `values.yaml` with `charts/`. Commit the distribution and matching values together. Keep credentials, node identities, image archives and build state outside the checkout. Keep committed values limited to image references and architecture selectors. `image-loader/` and `image-preparation/` are temporary source charts. `charts/` is a replaceable distribution directory.

Run `python3 -m unittest discover -s tests -v` from this directory. Tests mock image builds and cluster access. `package-charts.sh` checks placement synchronization and packages source charts into a fresh external output directory. It must not modify runtime sources or install releases.
