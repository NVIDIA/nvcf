---
name: run
description: >-
  Start the nvcf-ui development server. Use when asked to run, start, or launch
  the app, or to check a change in the browser.
license: Apache-2.0
allowed-tools:
  - Bash
---

# Running nvcf-ui

## Mock mode (use this for UI work)

```sh
task ui:dev MOCK=true
```

Starts Vite at `http://localhost:5173`. MSW answers every API request from the
seeded in-memory store. No cluster or backend is needed.

Layer a scenario over the store:

```sh
task ui:dev MOCK=true SCENARIO=functions:empty-list
```

Separate several scenarios with commas.

Set `MOCK` and `SCENARIO` as task variables as shown. The task sets
`VITE_MOCK` and `VITE_SCENARIO` itself, so exporting those variables has no
effect.

## Without mocks

`task ui:dev` without `MOCK=true` needs the backend for frontend running
locally. The dev server proxies only `/api` to `http://localhost:8080`. Other
API paths, such as `/v2/nvcf/...`, are answered with `index.html`, and the app
fails with a JSON parse error. Running the backend against live upstreams
needs in-cluster access; see `README.md`.
