# Cache set refresh: sets that learn what warm starts compile

Goal: when a warm worker compiles something the shared cache set does not
have, the next deployment gets it from the set instead of compiling it
again. Today a set is collected once, from the cold run, and never
changes; everything a warm start JIT-compiles later lives in one pod's
emptyDir and dies with it.

Measured on GB300, 2026-10-01, Nemotron Omni with the vLLM startup plan:
every first start spent 24 s compiling one FlashInfer kernel the cold run
never produced. Restarted containers, which still had that kernel in
their emptyDir, started in 62 to 70 s against 93 to 100 s. That 30 s is
what this mechanism recovers, for this and every later case of the same
shape (new request shapes, new graph sizes, engine upgrades that JIT
lazily).

Extends `helm-shared-model-volume.md`, mechanism 5. Same identity, same
collector, same election. One new idea: a set has generations.

```mermaid
flowchart TD
    A[warm worker Ready + settle] --> B{files in the cachedir newer than the seed marker?}
    B -- no --> Z[nothing to do]
    B -- yes --> C[agent on that node stamps cache-rank-delta and delta bytes on the pod]
    C --> D{all ranks of the group live and set complete and cooldown passed?}
    D -- no --> Z
    D -- yes --> E[any agent runs the election: atomic Create of claim nvsnap-cache-key-gN+1]
    E -- lost --> Z
    E -- won --> F[collector attaches the new claim via mount-holder]
    F --> G[streams every rank's full cachedir from a live warm pod of that ordinal]
    G --> H[marks generation N+1 complete with the delta fingerprint]
    H --> I[webhook mints read-only views from the newest complete generation]
    I --> J[next deployment seeds from N+1: no compile]
    H --> K[generation N retires when no reader is bound and retention passed]
```

## Decisions

Generations, not in-place writes. A set is immutable once complete, like
the model volume. Writing into a live set would need a read-write attach
while readers hold shared read-only attaches, which NVMesh does not
promise, and would hand a changing tree to pods mid-seed. A refresh
writes a new volume and the identity `cache://<hash>` gains a generation
label. Readers of the old generation are unaffected; the reaper retires
it once nothing is bound to it and the retention has passed, the same
rule it applies today. Sets are small (8 GiB for eight ranks here), so a
full copy per refresh is cheap.

Warm pods are the source, not the old set. A warm pod's cachedir is the
seeded rank plus everything it compiled since, which is exactly the new
rank content. The collector streams each ordinal's full cachedir from a
live warm pod through the existing `GET /v1/cache-rank/{key}/{ordinal}`,
the same path the cold collection uses. No attach of the old generation,
no merge logic. If a rank has no live pod the round is skipped; the next
warm deployment tries again.

Delta detection by the seed marker. The seed init already copies the
rank into the emptyDir with mtimes preserved and then writes a marker
file. After Ready and the settle period the agent lists files under the
cachedir newer than the marker. Any file means a delta; the count and
bytes are stamped on the pod as `nvsnap.io/cache-rank-delta` and
`cache-rank-delta-bytes`. Touched-but-unchanged files only cost a
harmless re-copy.

Bounded. A refresh needs: the set complete, at least one rank with a
delta, every rank of the group live, and no refresh of this set within
`agent.cacheVolume.refreshCooldown` (default 30 min). A loop guard
compares the delta fingerprint (sorted relative paths of the delta) with
the one recorded on the generation it came from; the same fingerprint
twice means the engine rewrites those files every start, the set is
marked `refresh-stable` and no further refresh runs until the salt or
the configuration changes.

## What changes

| Piece | Change |
|---|---|
| seed init (reader_steps.go) | writes `<cachedir>/.nvsnap-seeded` after the copy |
| agent, after Ready + settle | delta scan against the marker; stamps delta annotations; calls the existing election with a generation suffix |
| modelvolume Lookup | returns the newest complete generation; `Generation` on State |
| collector | unchanged streaming; marks the new primary with `generation`, `refreshed-from`, `delta-fingerprint` and the time |
| webhook complete branch | mints the read-only view from the generation Lookup returned (no change to the pod shape) |
| reaper | retires superseded generations with no bound reader after the normal retention; superseded generations never get a new view |
| values | `agent.cacheVolume.refreshCooldown: 30m`, `agent.cacheVolume.refresh: true` |

Nothing changes for the model volume, the cold capture, or a cluster
without warm deltas. A set that never gains files never refreshes.

## Failure behaviour

| Failure | Effect | Why it is bounded |
|---|---|---|
| collector dies mid-copy | generation N+1 never marked complete | existing abandoned-primary reaping; N stays the served generation |
| a rank's warm pod dies during streaming | round fails | claim times out and is cleaned as today; next deployment retries after cooldown |
| engine rewrites the same files every start | one refresh, then `refresh-stable` | loop guard on the delta fingerprint |
| two agents see the delta at once | one wins | atomic Create of the generation claim, same as the cold election |
| NVMesh attach slow | refresh takes longer | storage profile timeouts; readers never wait on a refresh |

## Verification

1. Unit: Lookup picks the newest complete generation; seed marker written;
   delta scan ignores files older than the marker; cooldown and loop
   guard; reaper retires a superseded generation only when unbound.
2. GB300: deploy Omni warm against the current set, confirm the delta
   stamp names the Mamba kernel directory, confirm generation 2 is
   collected and complete. Deploy again: no compile, graph capture 3 s,
   first-start engine time near 65 s, admission to Ready near 160 s.
3. Deploy a third time: no delta stamped, no refresh, generation count
   unchanged. Confirm generation 1 retires after retention.
