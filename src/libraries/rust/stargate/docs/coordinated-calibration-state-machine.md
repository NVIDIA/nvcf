# Coordinated Calibration

> Type: Reference. Source: Stargate registration and load-balancer state.

Each Stargate owns its own calibration state. Stargates do not replicate
calibration values.

Scope:

```text
(routing_key, cluster_id, model_id) on one Stargate
```

## State

```text
Missing
  -> first coordinated local registration
  -> Assigned(owner, token) + RUN(token)

Assigned(owner, token)
  -> owner heartbeat
  -> RUN(token)

Assigned(owner, token)
  -> other pylon heartbeat
  -> WAITING

Assigned(owner, token)
  -> matching SubmitClusterCalibration(owner, token, value)
  -> Complete(value)

Assigned(owner, token)
  -> owner disappears while sibling remains
  -> Missing

Complete(value)
  -> any local registration
  -> COMPLETE

Complete(value)
  -> last local registration disappears
  -> Missing
```

`WAITING` and `COMPLETE` carry no value. Only `RUN` carries the opaque token.

## Rules

- A pylon submits a calibration result only to the Stargate that assigned it.
- Normal pylon registrations contain runtime backend stats only.
- Stargate gates routing for a coordinated cluster until local calibration
  completes.
- Effective input capacity is:

```text
max(local_calibration_tps, sum(active_runtime_input_tps))
```

- The completed floor is never sent to pylons.
- The last local registration removal deletes the local floor.
- Replacement hardware for an absent cluster gets fresh local calibration.

## Pylon Lifecycle

```text
ConnectingUnavailable
  -> health succeeds
  -> AdvertisingActive

AdvertisingActive
  -> canary fails, upstream healthy
  -> Recovering

AdvertisingActive
  -> canary fails, upstream down
  -> ConnectingUnavailable

Recovering
  -> recovery canary succeeds
  -> AdvertisingActive

Recovering
  -> upstream health fails
  -> ConnectingUnavailable
```

Calibration work is not a pylon advertisement state. It is assigned work
against the healthy local upstream.

## Discovery

`WatchStargates.watch_stargate_urls` are recursive watch seeds. Entries in
`stargates` are concrete registration targets. Pylons register with every
concrete target.
