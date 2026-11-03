# Local function discovery

The scaling loop reads the active-function list from Cassandra by default.
The self-managed stack also defaults to Cassandra. To enable per-node
discovery in a specific deployment, set this in its environment values:

```yaml
functionAutoscaler:
  env:
    SCALING__ACTIVE_FUNCTION_SOURCE: local_discovery
```

Helm rolls the pods when this environment variable changes. Set it to
`cassandra` to restore the legacy read path. There is no hot reload.

In local mode, every node runs the existing discovery queries and retains
only function-version pairs assigned to its current Cassandra buckets.
The scaling loop waits for an initial successful discovery. The in-memory
registry retains observations for `cassandra.recently_invoked_ttl_seconds`
(30 minutes by default) and is rebuilt after a restart. Functions without
recent traffic, workers, or visible instances may not be rediscovered until
they produce another metric.

The existing Cassandra discovery writer, node membership, locks, health
checks, and positive-capacity TTL writes remain enabled in both modes. The
metrics backend still receives the existing discovery queries, now from
every node using the local mode. Query optimization and persistence are
separate changes.
