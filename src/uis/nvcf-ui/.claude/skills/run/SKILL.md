---
name: run
description: >
  Start the nvcf-ui development server. Use when asked to run, start, or launch the app,
  or to verify a change works in the browser.
license: Apache-2.0
allowed-tools:
  - Bash
---

# Running nvcf-ui

There is no local backend — the app either runs against a real cluster or uses MSW mocks.

## Mocked mode (default for local dev)

```bash
VITE_MOCK=true task dev
```

Starts Vite at `http://localhost:5173` with MSW intercepting all API requests using the seeded in-memory store. No cluster required. Use this for UI development.

To layer a scenario on top of the baseline store:

```bash
VITE_MOCK=true VITE_SCENARIO=functions:empty-list task dev
```

Multiple scenarios are comma-separated: `VITE_SCENARIO=functions:empty-list,tasks:launching-task`.

## Against a real cluster

```bash
task dev
```

No `VITE_MOCK` — requests proxy to the configured upstream. Requires cluster access. Check `ui/vite.config.ts` for the proxy target.
