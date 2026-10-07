# Temporary shared development images

`build.py` creates a fresh image set through the parent image helper and updates `values.yaml` only after every selected node is preloaded. Keep credentials, node identities, archives and build state outside the checkout. Keep committed values limited to image references, architecture selectors and the public default namespace.

Run `python3 -m unittest discover -s ../tests -p test_dev_images.py -v` from this directory. Use mocked builds in tests. Normal Helm installation must use the committed values without invoking Docker or image import.
