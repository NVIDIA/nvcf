# Temporary shared development artifacts

`build.py` creates a fresh image set through the parent helper, preloads selected nodes, packages all charts and then publishes `values.yaml` with `charts/`. Commit that small chart bundle and its matching values together. Keep credentials, node identities, image archives and build state outside the checkout. Keep committed values limited to image references, architecture selectors and the public default namespace.

Run `python3 -m unittest discover -s ../tests -p test_dev_images.py -v` from this directory. Use mocked builds in tests. Normal Helm installation must use these committed artifacts without invoking Docker or chart packaging.
