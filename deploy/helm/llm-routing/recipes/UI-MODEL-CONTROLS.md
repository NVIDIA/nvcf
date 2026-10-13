# Demo UI start and stop controls

This is the specification for a Start and Stop button on each model card. The decision logic already exists in `llm.py start` and `llm.py stop`, backed by `recipes/scale.py`. The UI backend reuses that logic and does not reimplement the capacity check. See [Start and stop an installed model](../ADVANCED.md#start-and-stop-an-installed-model-by-replica-count) for the command behavior.

## Behavior

A stopped model has every deployment at zero replicas. Its endpoint stays registered, so the card shows "Deployed, unhealthy". Start scales each deployment to one replica. The badge changes to "Deployed" once the model answers its first health probe, which takes several minutes for large models.

Start checks capacity before it changes anything. A start is allowed only when every stopped deployment of the model fits on its node, comparing requested GPUs, memory and CPU with what other pods leave free. The check is a snapshot and does not reserve capacity.

## Backend endpoints

The demo UI backend exposes three calls. Each takes the served model ID and return the same report shape that `llm.py --json` prints.

| Call | Purpose | Changes the cluster |
|------|---------|---------------------|
| `POST /api/models/{model}/start-check` | Run the capacity check | No |
| `POST /api/models/{model}/start` | Check, then scale up | Yes |
| `POST /api/models/{model}/stop` | Scale down | Yes |

Body options for start: `stopBlockers` (boolean, default false). The backend never stops other models unless this is true.

The report fields the UI reads are `status` (`ready`, `running`, `blocked`, `stopped`), `blockers` (list of messages), `holders` (models on the needed nodes, each with `release`, `model`, `node`), `toStart` and `toStop`.

Responses:

- `200` with `status` `ready` and the deployments started.
- `200` with `status` `running` or `stopped` when nothing needed to change.
- `409` with `status` `blocked` and the report, when capacity is missing. Nothing was scaled.
- `404` when no installed release serves the model.

## Cluster permissions

The demo UI service account has no RBAC today and does not mount a token. The backend needs a dedicated Role in the model namespace, kept separate from the read-only registry path:

- `deployments`: `get`, `list`
- `deployments/scale`: `get`, `update`, `patch`
- `pods`: `get`, `list`
- `inferenceendpoints`: `get`, `list`
- cluster scope: `nodes` `get`, `list`

It needs no access to secrets, Helm release data or other namespaces. Mount the service account token only into the backend container.

## Capacity messages

The UI shows the blocker text from the report and does not rewrite it. Examples:

- Not enough GPUs: "Node NODE cannot fit DEPLOYMENT (GPU: needs 1, 0 free). In use by MODEL."
- Not enough memory: "Node NODE cannot fit DEPLOYMENT (memory: needs 100.0 GiB, 72.2 GiB free). In use by MODEL."
- Unhealthy node: "Node NODE: NODE is not Ready."

When `holders` is not empty, show a secondary action "Stop MODEL and start this model". It calls start with `stopBlockers` set to true. The UI asks for confirmation first and lists every model that will stop.

## Button states

| Card state | Primary button | Notes |
|------------|----------------|-------|
| Deployed, unhealthy, check passes | Start | Enabled |
| Deployed, unhealthy, check blocked | Start (disabled) | Shows the blocker as a tooltip and in a notification when clicked |
| Starting | Starting... | Disabled. Poll the registry until Healthy or the timeout |
| Deployed (healthy) | Stop | Asks for confirmation |
| Stopping | Stopping... | Disabled |
| Not available | None | No endpoint or deployments to scale |

Run the start check when the card opens and again on click, because other workloads can claim a GPU in between. Use a notification (toast) for a blocked start, with the first blocker as its text and a "Details" link for the rest.

A start that passes the check can still stay Pending if a GPU is taken first. If a model has not become healthy after the timeout (default 60 minutes), show "Start is taking longer than expected" with the pod scheduling message from the cluster.

## Access control

Scaling a model affects every user of the gateway, so the buttons must not be available to every caller. Require an authenticated operator role for start and stop, and keep the check call available to any viewer. Record who started or stopped each model in the backend log with the model, release and result. Do not log tokens or request bodies.

## Helm drift

Scaling changes replicas outside Helm. A later `helm upgrade` without `--reuse-values` restores the chart's replica count, and a release with `suspended=true` is stopped by Helm itself. Treat Helm suspend as a separate, heavier operation that the UI does not expose.

## Testing

Reuse the cluster fixtures in `tests/test_llm_scale.py` for backend tests. Cover a ready start, a GPU block, a memory block, a block that names holders, a start with `stopBlockers`, a running model, an unknown model and a model that is partly running. Verify the permission boundary by checking that the service account cannot read secrets.
