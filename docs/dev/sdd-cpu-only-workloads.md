# Design document: CPU-only functions and tasks

Tracking issue: NVIDIA/nvcf#74.

This document explains the design in plain words. Each decision says what was built, why, and which
alternatives were not used. Section 6 lists the known limits and the planned next features.

## 1. What this is, in one minute

Today every function and task in NVCF runs on a GPU. Some work does not need one: small services,
data preparation, glue code. This feature lets a user deploy a function or create a task that runs
on ordinary CPU nodes, with no GPU.

The user does not see a new kind of API. The existing "gpu" field accepts one reserved name, CPU,
and a CPU shape such as NCP.CPU.8c32g takes the place of a GPU instance type. The work is inside the
platform: the control plane (ICMS) learns CPU shapes, the cluster agent (NVCA) finds CPU nodes,
offers them, builds pods without a GPU and keeps them off GPU nodes, and the CLI gets two new flags.

Everything is off by default. Until an operator turns it on, customer workload behavior and rendered
defaults stay the same. Some internal upkeep, such as classification records, runs either way.

## 2. Goals and non-goals

Goals:

- A user can run a function or task with no GPU, and it runs only on eligible non-GPU nodes.
- No change to how any existing GPU workload is placed or behaves.
- Works with what is in this repository and the self-managed stack. No hosted service is needed.
- Safe to adopt in pieces: each part is off by default and changes no customer workload behavior
  until the feature is switched on.

Not in this release:

- No new fields in the public function or task API.
- One CPU shape per cluster. Every instance in a cluster has that size, a workload that needs more
  than the shape cannot run there, and a workload that needs less still takes a whole instance
  (decision 12). Scheduling by each workload's own request is a planned follow-up (section 6).
- GPU and CPU workloads together in one cluster are not supported (see decision 16). Grouping the queue by consumer is a planned follow-up (section 6).
- No model cache for CPU-only workloads.
- Shared cluster mode and host isolation do not cover CPU nodes.
- No new public usage API or dashboard for CPU functions and tasks. The GPU usage views leave CPU
  out. The only addition is an operator-facing status field on the cluster agent (status.cpuUsage)
  that shows what the CPU pool is using (decision 20).
- Deploying one function across several specs, with a CPU spec and a GPU spec on separate clusters,
  is out of scope. It is not documented and not tested. This is separate from GPU and CPU nodes in
  one cluster, which is held (decision 16). The CLI also deploys one spec per command (decision 21).

## 3. The big picture

How a CPU request travels:

```text
     User / CLI                 ICMS                  Queue                 NVCA                CPU node
                           (control plane)        (NATS or SQS)    (agent in the cluster)    (pod runs here)
          |                       |                     |                     |                     |
1. The user asks for a CPU function
          |                       |                     |                     |                     |
          | gpu CPU,              |                     |                     |                     |
          | NCP.CPU.8c32g         |                     |                     |                     |
          |---------------------->|                     |                     |                     |
          |                       |                     |                     |                     |
          |                       | picks a cluster     |                     |                     |
          |                       | with this shape     |                     |                     |
          |                       |                     |                     |                     |
2. ICMS sends the request to that cluster
          |                       |                     |                     |                     |
          |                       | creation message    |                     |                     |
          |                       |-------------------->|                     |                     |
          |                       |                     |                     |                     |
          |                       |                     | NVCA reads it       |                     |
          |                       |                     |<--------------------|                     |
          |                       |                     |                     |                     |
3. NVCA decides what kind of work it is
          |                       |                     |                     |                     |
          |                       |                     |                     | CPU-only? one       |
          |                       |                     |                     | resolver decides    |
          |                       |                     |                     | CPU held now?       |
          |                       |                     |                     | no: builds a pod    |
          |                       |                     |                     | with no GPU and     |
          |                       |                     |                     | a no-GPU node rule  |
          |                       |                     |                     |                     |
4. The pod is admitted and placed
          |                       |                     |                     |                     |
          |                       |                     |                     | webhooks check      |
          |                       |                     |                     | the trusted record  |
          |                       |                     |                     |                     |
          |                       |                     |                     | pod: shape label,   |
          |                       |                     |                     | no GPU request      |
          |                       |                     |                     |-------------------->|
          |                       |                     |                     |                     |
5. NVCA reports back
          |                       |                     |                     |                     |
          |                       | status of the request                     |                     |
          |                       | and of its instances|                     |                     |
          |                       |<------------------------------------------|                     |
          |                       |                     |                     |                     |
```

How a cluster comes to offer its CPU pool:

```text
      Operator                      CPU nodes                       NVCA                          ICMS
                             (labeled by the admin)        (agent in the cluster)            (control plane)
          |                             |                             |                             |
1. The operator sets things up once
          |                             |                             |                             |
          | declares the one CPU shape  |                             |                             |
          | (cpuPoolConfigB64)          |                             |                             |
          | labels the CPU node group   |                             |                             |
          | with that shape             |                             |                             |
          |                             |                             |                             |
2. NVCA finds the nodes that really count
          |                             |                             |                             |
          |                             | list nodes with the         |                             |
          |                             | shape label                 |                             |
          |                             |<----------------------------|                             |
          |                             |                             |                             |
          |                             |                             | a node counts only if:      |
          |                             |                             | Ready, no GPU evidence,     |
          |                             |                             | taints ok, big enough,      |
          |                             |                             | Ready for 5 minutes         |
          |                             |                             |                             |
3. NVCA waits for the control plane
          |                             |                             |                             |
          |                             |                             | is CPU READY for me?        |
          |                             |                             |---------------------------->|
          |                             |                             |                             |
          |                             |                             | READY (or not)              |
          |                             |                             |<----------------------------|
          |                             |                             |                             |
          |                             |                             | also needs the sentinel     |
          |                             |                             | (classification records     |
          |                             |                             | are complete)               |
          |                             |                             |                             |
4. NVCA offers the pool
          |                             |                             |                             |
          |                             |                             | shape minus infra overhead  |
          |                             |                             | capacity = how many shapes  |
          |                             |                             | fit on each node, summed    |
          |                             |                             |                             |
          |                             |                             | register entry named CPU    |
          |                             |                             | (GPU entries unchanged)     |
          |                             |                             |---------------------------->|
          |                             |                             |                             |
5. Held states keep GPUs safe
          |                             |                             |                             |
          |                             |                             | GPU nodes present,          |
          |                             |                             | shared cluster mode, or     |
          |                             |                             | ICMS not READY: CPU is      |
          |                             |                             | held, GPU is untouched      |
          |                             |                             |                             |
```

## 4. Principles that every decision follows

1. Off by default. New workload behavior happens only after an operator turns a switch on. With it
   off, customer workloads and their rendered defaults are unchanged. Some internal upkeep, such as
   the classification records of decision 4, runs whether the switch is on or off.
2. Never guess from a zero. A GPU count of zero does not mean "CPU". Today a zero is treated as one
   GPU in places. CPU is recognized only when the name, the instance type form and a zero count all agree
   (decision 3).
3. Fail closed. If the platform cannot tell whether work is CPU or GPU, it stops with a clear error.
   It never quietly treats the work as GPU.
4. Trust only what a workload cannot edit. A user's pod can change its own labels and annotations,
   so the platform never believes them for this decision.
5. The control plane ships first. A new agent never offers CPU to an older control plane that cannot
   handle it.
6. GPU service is never put at risk. When CPU has to be held back, GPU work keeps running.

## 5. Design decisions

### Decision 1: CPU is a reserved GPU name, not a new API

What we chose: the existing gpu field accepts the reserved name CPU. A CPU instance type is named
<provider>.CPU.<shape>, has gpuCount 0, and carries cores, memory and storage.

Why: NVCF and NVCT only compare these strings against the ICMS catalog. They never read the GPU
count. So no API or database shape has to change. The work stays in ICMS, the agent and the shared
translation library.

Not chosen: new request fields for CPU. That would touch every API and client.

Case: only the upper case name CPU is supported, because the platform stores the spelling the
caller used while some lookups are case sensitive. The CLI accepts any case and sends CPU.

Component: ICMS (control plane) and the CLI.

### Decision 2: everything is behind switches, off by default

What we chose: a switch in ICMS (icms.cpu-workloads.enabled), a feature flag in the agent
(CPUOnlyWorkloads), and a stack value (cpuWorkloads.enabled). New fields use omitempty, the chart
renders new keys only when set, and existing golden files are not edited.

Why: an installation that does nothing must behave exactly as before, and every PR must be safe to
merge alone.

Component: all components.

### Decision 3: CPU is recognized by three things together

What we chose: one function, IsCPUOnlyInstanceType, says an instance type is CPU-only only when all
three hold: the GPU name is exactly CPU, the instance type has the form <provider>.CPU.<shape>, and
the GPU count is zero. Input that matches only some of these is not CPU-only and returns false. The
function returns an error (ErrCannotClassify) only for ambiguous input: an empty GPU name, a CPU name
in a non-canonical letter case, or CPU with no instance type. It never guesses CPU from a zero.

Why: a zero alone is ambiguous. Some old messages leave the count empty and rely on it meaning one
GPU. Requiring the name as well means a malformed GPU entry with count zero is never mistaken for
CPU, and a CPU shape never gets a GPU name made up for it.

Component: ICMS, classification records and translation.

### Decision 4: the trusted classification record lives outside the workload namespace

What we chose: for every Helm workload namespace, NVCA writes one small immutable ConfigMap in its
own namespace. It says cpuOnly yes or no, and holds the namespace UID and the MiniService UID. The
webhooks read this record.

Why: the admission webhooks must know if a namespace is CPU-only, and the answer must not be
something a user can edit. The metadata ConfigMap inside the workload namespace can be changed by
workload identities, so it cannot be trusted. The NVCA namespace is out of their reach. The UID check
catches a namespace that was deleted and recreated under the same name.

Not chosen: protecting the in-namespace ConfigMap with an admission rule. A rule that blocks deletes
would also block the namespace controller and could leave namespaces stuck in Terminating.

Details that matter:

- A record exists for every namespace, GPU ones too (cpuOnly false). A record only for CPU
  namespaces could not tell "GPU namespace" from "missing record".
- Records are create-only, deterministic and deleted with UID and version preconditions, so two
  agents during a rolling update cannot corrupt one.
- A record that disagrees with its workload is never repaired in place. It is marked quarantined and
  admission treats it as unusable. Changing class needs a new namespace.
- A sentinel ConfigMap means "the initial backfill of records finished". Before it exists, the
  enforcing webhook does not deny a namespace for a missing record, so a first upgrade never denies
  running GPU work. Once it exists, a missing or unreadable record fails closed. The sentinel is
  create-only: a second creator hits a conflict instead of overwriting it, and it is not rebuilt on
  each restart, because a rebuilt sentinel would reopen the fail-open window after every restart. It also
  carries schemaVersion and minReaderVersion. Two rules fence an older agent. If minReaderVersion is
  newer than the agent understands, it cannot read the sentinel. If the sentinel is readable but its
  schemaVersion is newer than the agent writes, the agent also stops writing. Either way the agent
  becomes read-only, so an older agent in a rolling update cannot corrupt newer records.
- Record upkeep does not depend on the feature flag, so turning the flag off and on again never
  leaves gaps.

Component: NVCA classification records.

### Decision 5: two trusted signals, one for each kind of workload

What we chose: for operator Helm workloads the signal is the classification record. For legacy
container workloads, which have no record, the translator marks the pod with an annotation, and the
webhooks honor it only when the request comes from NVCA's own service account in a container
namespace.

Why: Helm chart authors control pod labels and annotations, so Helm pods are never trusted by
their own metadata. Container pods are built by NVCA itself, so NVCA's identity can be checked.

Component: NVCA translation and webhooks.

### Decision 6: fail closed

What we chose: a request that cannot be classified, or a CPU-only request for an unknown instance
type, ends with a terminal error. A CPU-only request while the feature flag is off also fails
clearly, so the flag is a real kill switch and a request queued before a rollback is never run as a
GPU workload. The Helm validating webhook denies when it cannot read a valid record in a Helm
namespace. The mutating webhooks fall back to today's behavior, which is safe because the
validating webhook is the enforcement point.

Why: the bad outcome is a CPU workload getting GPU resources, or a GPU workload losing its guard.
Stopping is better than guessing.

Component: NVCA classification records, translation and webhooks.

### Decision 7: no GPU in a CPU pod, enforced by checks and by placement

What we chose: a CPU-only pod carries no nvidia.com resource key at all, even with value zero. The
Helm validating webhook rejects any nvidia.com resource and any pod that sets nodeName in a CPU-only
namespace. No max-gpus quota is created.

The honest limit: the CPU rules apply to every identity, but the webhook only covers the resources
it is registered for: pods, services, service accounts, deployments, statefulsets, roles, role
bindings, jobs and cron jobs. A controller that creates a pod through another resource, such as a
ReplicaSet, is not seen. So the real guard is placement. CPU nodes advertise no GPU resource, and the
pod is pinned to the CPU shape, so a pod that still asked for a GPU could not be scheduled.

Not chosen: a zero GPU quota, and relying on the webhook alone.

Component: NVCA webhooks and translation.

### Decision 8: how a CPU pod is built and kept apart from GPU nodes

What we chose, in the translation library (CPUOnly mode) and in NVCA:

- No GPU toleration is added. The seven places that added it go through one helper that skips it for
  CPU-only work and is otherwise identical.
- Every CPU-only pod requires a node that has the shape label and no GPU label. So a GPU node that
  an admin labeled with a CPU shape by mistake is never chosen.
- CPU, memory and storage requests are always kept, even when the resource enforcement flags are
  off, because a CPU pod with no requests is not schedulable sensibly.
- NVLink, DRA and Nsight profiling are skipped. NVSnap is skipped.
- A GPU name is never derived from a CPU shape. A message that lost its GPU name fails loudly.

Why: CPU and GPU pods stay apart through the required node rule on the instance type label, not
through tolerations.

Component: the translation library and NVCA.

### Decision 9: the CPU pool is declared in configuration, one shape per cluster

What we chose: the operator declares one CPU shape in the chart value cpuPoolConfigB64, in the same
format as a GPU instance type with gpuCount 0. The shape is authoritative from this declaration, not
from whatever nodes exist. Cluster registration offers exactly that shape.

Why: a fixed declared shape is predictable. Users see one stable shape. Node sizes decide how many instances fit (decision 12).

Checks: the shape must have three parts with CPU in the middle, be a valid Kubernetes label value
(it becomes a label selector), and have positive cores, memory and storage. The same validator runs
in the cluster mapper, the operator, the config merge and agent startup, so an NVCFBackend written
directly cannot sneak in a bad pool. A pool needs dynamic GPU discovery. Static discovery is
rejected because ICMS ignores the GPUs in a registration without it.

Not chosen: several shapes in one cluster (documented as a limit), and shapes discovered from nodes.

Component: NVCA.

### Decision 10: CPU nodes are found by a label the admin applies

What we chose: a second node informer selects nodes whose instance type label equals the declared
shape. The admin puts that label on the CPU node group once, in the node group template. NVCA only
reads the label and never writes it.

Why: the same label already pins CPU pods through the required node rule, so one label drives both
discovery and placement. NVCA needs no new permission to write nodes.

Not chosen: NVCA labeling nodes automatically. It would need write permission on nodes, ownership
and cleanup logic, and would put NVCA in charge of the admin's node groups. The existing GPU node
informer is also left alone, because shared cluster mode, host isolation, the GPU monitor and others
read it.

Component: NVCA.

### Decision 11: which nodes count

What we chose: a labeled node counts toward capacity only when it is Ready and schedulable, has no GPU
label or GPU resource, has no taint the workload tolerations do not cover, and has enough allocatable
cpu, memory and storage for the declared shape. The absence of GPU evidence only counts after GPU
discovery has synced and the node has been Ready for five minutes.

Why the five minutes: GPU labels and device plugin resources appear some time after a node joins. A
node that looks like a CPU node in its first moments may turn out to be a GPU node. The time is
measured from the node's Ready transition, so restarting NVCA does not reset it. A missing or future
timestamp counts as still settling.

One function decides this for capacity and for the schedulable signal, so they cannot disagree.

Component: NVCA.

### Decision 12: capacity is counted in instances, as many as fit on each node

What we chose: a CPU instance is one copy of the declared shape, and the cost of a request is its
instance count. For each eligible node, the number of instances is the smallest of
floor(allocatable cpu / shape cpu), floor(allocatable memory / shape memory) and
floor(allocatable storage / shape storage). The cluster's capacity is the sum over the eligible
nodes. A node that cannot hold even one instance adds nothing. Each function instance or task takes
one instance. The infra overhead (the platform's own sidecars) is taken out of the declared shape
before it is registered, so it is not subtracted again per node.

Why: a CPU node is much larger than one small workload, so counting a whole node as one instance
would waste it. Dividing the node by the shape packs as many instances as the node really has room
for, and the declared shape keeps what one instance may use fixed and predictable. The workload and
the platform's containers must fit in the slice that the shape describes. With the default overhead
of about 4.75 cores and 6.1Gi, a 4 core shape would be too small to register, so the docs use
NCP.CPU.8c32g. A shape no larger than the overhead is not registered.

Note on sizing: the shape is the size of every instance in the cluster. A workload that needs less
than the shape still takes a whole instance, and a workload that needs more than the shape cannot run
in that cluster. Pick the shape for the largest workload you expect, and size nodes as a multiple of
it. The dimension that runs out first decides how many instances a node holds, so memory or storage
can limit a node whose cpu would allow more.

Next feature: schedule by each workload's own CPU and memory request, or offer more than one shape in
a cluster, so that small workloads do not each take a full instance. That changes capacity
accounting, registration and placement, and it needs its own design (section 6).

Component: NVCA.

### Decision 13: the control plane gates the CPU pool

What we chose: ICMS exposes DISABLED, READY or BLOCKED. NVCA offers CPU only while ICMS says READY
and the classification sentinel exists. It reads the status every minute while ready and every 15
seconds while not. An error never counts as ready, so an ICMS that predates the feature keeps CPU
held and GPU registration unchanged. If ICMS refuses a registration with CPU_WORKLOADS_NOT_READY,
NVCA registers again with the GPU types only.

Why: the control plane ships first, and a new agent must never offer CPU to a control plane that
cannot handle it.

A detail: ICMS refuses a registration that drops an instance type that still has active instances. So
while CPU is held, a CPU entry that a live CPU workload still needs stays registered. It is kept only
for as long as that workload exists, and never admits a new request, because admission checks the
same hold.

Component: ICMS and NVCA.

### Decision 14: a cluster with only CPU nodes works

What we chose: the GPU monitor tracks schedulable capacity (GPUs or an eligible CPU pool) apart from
the physical GPU signal. The physical signal stays truthful: it still says there are no GPUs. The gpu
health component is healthy with no GPUs while the CPU pool has capacity. Registration refuses to send
an empty GPU list, which ICMS rejects. The cluster validator passes on a CPU pool.

Why: a new CPU-only cluster has no GPUs, so without this it would fail to start or be marked
unhealthy.

The GracefulNoGPU feature gate is required on a cluster with no GPU nodes. Without it the agent exits
on its first start, because it finds no GPU and has not yet recorded the classification records and
the sentinel that the CPU pool needs before it is offered. With the gate on, the agent starts with no
capacity, records them, and then offers the pool.

Component: NVCA.

### Decision 15: shared cluster mode holds the CPU pool

What we chose: when shared cluster mode is on, CPU is held. It is detected at runtime from node
labels, and the detection also reads an unfiltered node informer so CPU-only nodes are seen.

Why: shared cluster mode adds a required schedule=true rule to every pod. CPU pods would go Pending
silently. A warning level cpupool health component says why, without making GPU workloads unready.
Host isolation does not cover CPU nodes and is documented as unsupported with a CPU pool.

Component: NVCA.

### Decision 16: GPU and CPU in one cluster are held, not supported

What we chose: if a cluster has GPU nodes and a CPU pool, NVCA holds the CPU pool. It registers no CPU
capacity, refuses new CPU requests, shows the reason in the cpupool health component, and keeps
serving GPU work. The cluster validator reports the same case.

Why: the creation queues of a cluster share one consumer, and the agent counts each message against
whichever loop fetched it. In a mixed cluster a CPU request could be counted against GPU capacity, or a
GPU request refused against CPU capacity. That is unsafe. Grouping the polling by
consumer fixes it properly and is a planned follow-up (section 6).

What happens to CPU requests already queued when the hold starts: they must not block GPU work, and
they must not be lost. The path is:

1. The request is recorded in the cluster as an ICMSRequest in a new RequestRejected status, with no
   instances and a reason. This is the durable record.
2. The queue message is acknowledged once that record exists.
3. The agent's ICMS loop tells ICMS "cannot fulfill" for each record, retries every pass until ICMS
   accepts it, and then moves the record to the existing failure acknowledged state, which the
   existing cleanup removes later. A 400, 404 or 412 closes the record.
4. Nothing creates instances for a rejected request, and it counts as holding no capacity.

Alternatives that were not used:

- Hand the message straight back: JetStream redelivers it ahead of the GPU messages, so it blocks them.
- Keep it unacknowledged for a while ("parking"): every parked message holds a slot on the shared
  consumer, and NATS stops delivering at 1000 pending, so a big backlog still blocks GPU work.
- A local cap and drop the rest: silent data loss, and a count that is per process, not per consumer.
- Tell ICMS first, then acknowledge: an ICMS outage would put the message back at the front.

The durable record means an ICMS outage cannot hold up the queue.

Component: NVCA.

### Background for decision 16: how the NATS queue works

Decision 16 only makes sense with a picture of the queue, so here it is.

The queue. In a self-managed install, ICMS puts creation messages on a NATS JetStream stream. NVCA
reads them through one durable consumer for each stream and cluster, named from the stream and the
cluster id, with a single subject filter. NVCA runs one polling loop per GPU name (in a self-managed
install, one for each entry in its usage map), and all of them read from that same stream and the same
consumer. The NATS client even carries a note that a consumer per GPU type is still an open question.

Why that matters. Whichever loop fetches a message counts it against its own GPU name. That is fine
when a cluster has one GPU type. With two types, a message for one can be counted against the other.
Adding CPU as one more name is the same problem, which is why GPU and CPU in one cluster are not
supported in this release.

Four NATS words:

- Pending: a message that was delivered and not yet acknowledged.
- Acknowledge: tell NATS the message is done. It leaves the consumer for good.
- Nak: give the message back now. NATS redelivers it at once, and ahead of newer messages.
- InProgress: ask for more time. The message stays pending until the ack wait ends. For the creation
  queue that wait is 360 seconds. NATS also keeps a message that was Nak'd with a delay in the pending
  set until it is redelivered.

The limit. A consumer stops delivering when too many messages are pending. The default is 1000. It is
one number for the whole consumer, shared by every agent replica, and the server can be configured with
a lower one. When the limit is reached, no later message is delivered, whatever its type.

What the GPU path does today when it cannot serve a request: it makes the message visible again at once
(a Nak) and sets a random backoff of up to 50 passes for that loop. Nothing is left pending, so a GPU
backlog sits in the stream and never reaches the limit. The price is that a refused message comes back
first. This is existing behavior and it is unchanged for GPU-only clusters.

Held CPU requests need something better, because a refused message that keeps coming back first would
block the GPU messages behind it. What each approach does:

- Nak it at once: it comes back first, every time, and blocks the GPU messages behind it.
- Keep it pending for a while: it holds a slot. Enough of them reach the limit, and then no message of
  any kind is delivered.
- Cap how many are kept and drop the rest: the cap is per process while the limit is per consumer,
  restarts and extra replicas break it, and a dropped request is lost without a trace.
- Tell ICMS first, then acknowledge: during an ICMS outage the call fails, the message is Nak'd, and it
  is at the front again for as long as the outage lasts.

What we do instead is shown here:

```text
     NATS stream                Queue manager                  Cluster                      ICMS
(one shared consumer)             (in NVCA)             (ICMSRequest records)          (control plane)
          |                           |                           |                           |
1. A CPU request arrives while CPU is held
          |                           |                           |                           |
          | fetch next message        |                           |                           |
          |<--------------------------|                           |                           |
          |                           |                           |                           |
          | a CPU request             |                           |                           |
          |-------------------------->|                           |                           |
          |                           |                           |                           |
2. It is recorded, then acknowledged
          |                           |                           |                           |
          |                           | create a record:          |                           |
          |                           | status RequestRejected    |                           |
          |                           |-------------------------->|                           |
          |                           |                           |                           |
          | acknowledge               |                           |                           |
          | (only after the record)   |                           |                           |
          |<--------------------------|                           |                           |
          |                           |                           |                           |
3. The queue is not held up
          |                           |                           |                           |
          | fetch next message        |                           |                           |
          |<--------------------------|                           |                           |
          |                           |                           |                           |
          | a GPU request             |                           |                           |
          |-------------------------->|                           |                           |
          |                           |                           |                           |
          |                           | served as usual           |                           |
          |                           |                           |                           |
4. Later, ICMS is told (retried until it works)
          |                           |                           |                           |
          |                           |                           | cannot-fulfill            |
          |                           |                           |-------------------------->|
          |                           |                           |                           |
          |                           |                           | accepted                  |
          |                           |                           |<--------------------------|
          |                           |                           |                           |
          |                           |                           | record moves to           |
          |                           |                           | FailureAcknowledged,      |
          |                           |                           | cleaned up later          |
          |                           |                           |                           |
```

Why it works:

- The message is acknowledged as soon as a durable record exists in the cluster, so it never holds a
  slot and never comes back first.
- ICMS is not on the queue's path at all. The agent's ICMS loop reports the record later and keeps
  trying, so an ICMS outage cannot hold up GPU work.
- ICMS already has a request level status for this, cannot-fulfill. It needs the message batch id and is
  recorded against that batch. A 400, 404 or 412 reply means ICMS cannot or will not record it, so the
  record is closed.
- A restart between creating the record and acknowledging the message is handled: when the message is
  delivered again, the agent sees the recorded rejection and acknowledges it, without making a second
  record. If the record cannot be created, the message is made visible again.
- GPU-only clusters are unchanged: the new code only runs for a CPU request while CPU is held.

How it is tested. The test starts a real embedded JetStream server and sets the pending limit to 3 on
purpose. It first shows that three unacknowledged messages stop all delivery. Then, with ICMS failing
the whole time, it feeds 10 held CPU requests and one GPU request behind them. All 10 are recorded and
acknowledged, the GPU request arrives, the records survive the outage, and once ICMS recovers each one
is reported and closed. Removing the acknowledgement, the agent loop branch, or the rule that a rejected
request holds no capacity makes it fail. The client got a small option for this, WithMaxAckPending, left
at the server default in production.

What this does not solve. Splitting the queue polling by consumer, so that GPU and CPU can share a
cluster, is the follow-up work. Until then the hold and this rejection path keep the two kinds of work
from harming each other.

### Decision 17: the agent restarts with a safe update strategy

What we chose: with CPUOnlyWorkloads on, the operator builds the agent Deployment with the Recreate
strategy, as GracefulNoGPU already does. The strategy follows the effective feature flags, which are
the operator's flags with the agent-config-merge ConfigMap merged over them, read the way the agent
reads them (a plus or minus prefix, the last entry wins).

Why: with a rolling update an old webhook replica that does not know about CPU could serve admission
while the new agent already offers CPU capacity. Recreate leaves no old replica. The strategy has to
follow the effective flags, because the merge ConfigMap can turn the flag on without the backend's own
flags saying so.

Component: the NVCA operator.

### Decision 18: the control plane (ICMS) rules

What we chose, when the switch is on:

- A registration with a CPU shape is validated on every path. The name and value must be the same
  shape name and a valid label, cores positive, memory and storage positive Kubernetes quantities.
  A cluster registers exactly one CPU shape, and only under the name CPU.
- A real GPU already named CPU, in any letter case, makes the state BLOCKED until it is renamed.
- A zero GPU count stays zero, and capacity is counted in instances.
- Usage events do not bill CPU instances as GPU hours. Reservations for a CPU shape are refused.
- CPU metrics are separate. The GPU-named metrics leave CPU out (decision 20).
- Outside READY, a CPU shape is refused with HTTP 409 CPU_WORKLOADS_NOT_READY, with no silent
  processing as a GPU type.

Why the strict shape check: a loose check could accept shapes the agent cannot build, and ICMS would
then route work to a cluster that cannot run it. The check mirrors what the agent requires.

Component: ICMS.

### Decision 19: no model cache for CPU-only workloads

What we chose: a CPU-only request that asks for a model cache fails with a clear terminal error.

Why: the cache writer job and the cache server have no instance type placement and run in a shared
namespace that adds a GPU toleration. Whether their volumes can be mounted from CPU nodes was not
verified. Revisit only if users ask.

Component: the translation library and NVCA.

### Decision 20: what is measured and shown

What we chose: the agent reports the CPU pool's usage on the NVCFBackend in a separate field,
status.cpuUsage, set only while the pool is offered. The pool also stays in the usage map under the
name CPU, in instances, because ICMS reads that entry. The CLI uses status.cpuUsage to tell the pool
apart from a real GPU that happens to be named CPU, so such a GPU stays a GPU. The cluster agent
status shows a separate CPU pool table in instances, validate-deployment gains a cpu-utilization
check, and a cluster with no CPU pool shows exactly what it showed before.

Why: mixing instances and GPUs in one total gives wrong numbers and false warnings.

Component: NVCA and the CLI.

### Decision 21: the CLI uses two dedicated flags

What we chose: --cpu-only (a switch that must be true) and --cpu-shape on function deploy create,
function deploy update and task create. The CLI sends the reserved name CPU, so users never type it.
The flags need each other and conflict with --gpu, --gpu-name, --instance-type and --input-file, and
the error names every conflicting flag. The shape must be <provider>.CPU.<shape>.

Why: overloading --gpu would leak its H100 defaults into a CPU request. Dedicated flags make the intent
explicit. The CLI deliberately manages one specification per command. Mixed CPU and GPU
specifications are out of scope and untested.

Component: the CLI.

### Decision 22: the stacks refuse the switch on versions that cannot work

What we chose: the charts and images the stacks pin predate the feature (SIS chart 2.6.0, NVCA chart
1.28.0, NVCA images 3.10.0). So the stacks refuse cpuWorkloads.enabled=true until compatible versions
are named. The self-managed stack needs cpuWorkloads.sisChartVersion or the existing sis.chartPath.
The compute-plane stack needs cpuWorkloads.chartVersion or cpuWorkloads.chartPath, and
cpuWorkloads.imageTag, which sets the operator image tag and the agent version together. Giving a
version without the switch, or two ways at once, is also refused.

Why: a documented switch that quietly produces a deployment that cannot work is worse than a clear
error. A warning in the notes is not enough.

Not chosen: bumping the pins together with this feature. That needs a release of the charts and
images first (section 7).

Component: the self-managed and compute-plane stacks.

### Decision 23: events for CPU pods still find their node

What we chose: the function deployment stage events look up a pod's node to read image sizes. CPU pool
nodes are not in the GPU filtered node lister, so the lookup falls back to the CPU pool lister.

Why: otherwise stage events for CPU workloads would be dropped.

Component: NVCA.

### Decision 24: how we tested

What we chose:

- Unit tests for every rule, and golden tests for every translation mode.
- envtest suites against a real API server for the records, the sentinel, preconditions, the
  replacement race, immutability, quarantine, namespace reuse, Terminating namespaces, restart and
  rollback, and the webhook checks.
- ICMS tests through the real controllers, and a full Bazel run with JDK 25.
- A real embedded JetStream test with a small MaxAckPending, an ICMS outage and a CPU backlog, to prove
  GPU requests still arrive. Skipping the acknowledgement, the loop branch or the no-capacity rule each
  makes it fail.
- Mutation checks: for each important rule, breaking it on purpose makes a test fail.
- Differential tests where one reader must match another, for example the feature flag reader against
  the agent's own parser.
- BDD: an opt-in feature on a cluster with no GPU nodes, covering function create, update and invoke,
  placement by function ID, and a task run to completion with the node of its own pod. It is skipped
  unless BDD_CPU_WORKLOADS=1 because it needs charts and images that carry the feature.

### Decision 25: documentation and the agent skill

What we chose: a CPU-only workloads page, short notes on the other pages, an operator section in the
compute plane configuration (including how it differs from HelmAllowCPUNodes), and the CLI agent skill
updated, with the embedded skill data regenerated, never edited by hand.

Component: the docs and the CLI agent skill.

## 6. Known limits and next features

Limits in this release:

- One CPU shape per cluster. Every instance has that size. A workload that needs more than the shape
  cannot run in that cluster, and one that needs less still takes a whole instance (decision 12).
- GPU and CPU workloads in one cluster are not supported. The CPU pool is held when GPU nodes are
  present (decision 16).
- A cluster with no GPU nodes needs the GracefulNoGPU gate on its first start (decision 14).
- No model cache, no shared cluster mode and no host isolation for CPU nodes.
- The webhook cannot see every controller path. Placement is the real guard (decision 7).
- The live CPU BDD scenarios are opt-in. They need charts and images that carry the feature.

Next features:

- Finer CPU scheduling. Schedule by each workload's own CPU and memory request, or offer more than
  one shape in a cluster, so that small workloads do not each take a full instance and large ones can
  run. This needs changes to capacity accounting, registration and placement, and its own design.
- Hybrid clusters. Give each class of creation message its own consumer, so that GPU and CPU
  workloads can share a cluster (decision 16).
- Live end to end validation and release. Run the CPU BDD against released charts and images, then
  bump the pins in both stacks (section 7).

## 7. Release dependencies

The control plane ships first. Then the charts and images that carry this feature must be released,
and the pins in both stacks bumped: the SIS chart, the NVCA operator chart, and the NVCA operator and
agent images. Until then the stacks refuse the switch.

## 8. Words used in this document

- ICMS: the control plane service that tracks clusters, instance types and requests.
- NVCA: the agent in each cluster that registers capacity and runs requests.
- Shape: the name of a CPU instance type, for example NCP.CPU.8c32g.
- Instance: one running copy of a function or task. In a CPU pool, one instance is one copy of the
  declared shape, and a node holds as many as fit.
- Classification record: the trusted per-namespace note saying CPU-only or GPU.
- Sentinel: a marker saying the initial backfill completed, so missing records now fail closed.
- Hold: NVCA deliberately not offering or accepting CPU work for a stated reason.
- Webhook: a check that runs when Kubernetes objects are created or changed.
- Consumer: the shared reader of the cluster's creation queue.
- JetStream: the NATS feature that stores messages and tracks which were delivered and acknowledged.
- Pending: a message delivered and not yet acknowledged. A consumer stops delivering at its pending limit.
