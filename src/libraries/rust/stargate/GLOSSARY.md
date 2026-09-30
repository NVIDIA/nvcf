# Stargate

Stargate routes inference requests to registered inference capacity.

## Language

### Routing target

The model and optional routing key that identify the inference capacity a
request may use.

### Cluster

A group of inference backends that shares routing statistics and capacity
estimates.

### Backend

A registered inference server within a cluster that can receive requests.

### Request routing

The selection of a cluster and backend for one inference request, including
capacity waits and exclusions after failed attempts.

### Routing wait

A delay before reevaluating inference capacity for a routing target. It is
distinct from an upstream retry, which repeats an attempt to send the request.
