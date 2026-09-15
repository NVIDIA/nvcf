# AGENTS.md

## Project Overview

nv-cloud-function-helpers is a small Python library that wraps common tasks for
code running inside an NVCF container: receiving inference requests, shaping
responses, and interacting with NVCF runtime conventions. The shipped module is
`nv_cloud_function_helpers.nvcf_container`.

Language: Python (3.8 through 3.11 per `pyproject.toml` black config).
License: Apache-2.0, covered by the umbrella repo-root `LICENSE`.

## Install

From a local checkout of this umbrella repo:

```
pip3 install setuptools
pip3 install ./src/libraries/python/nv-cloud-function-helpers
```

## Runtime dependencies

Declared in `setup.py`:

- Pillow >= 10.0.0

## Layout

- `nv_cloud_function_helpers/` Python package (nvcf_container submodule).
- `setup.py`, `pyproject.toml` packaging metadata.

## Tests

No test suite is present yet. Add pytest coverage alongside changes when
modifying helpers.
