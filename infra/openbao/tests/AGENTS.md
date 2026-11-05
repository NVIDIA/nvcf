# OpenBao lifecycle tests

Run `python3 -m unittest discover -s infra/openbao/tests` from the repository
root. Tests use temporary plugin files and a stateful fake `bao`; they do not
contact a server. Keep real upgrade, signing, and rollback qualification
separate from these command and state checks.
Chart integration tests also use a fake `kubectl` to check bootstrap guards
and member selection without contacting a Kubernetes cluster.

`Dockerfile` builds a disposable runtime test image from an explicit bridge
image and the source Dockerfile's pinned Go image. `runtime-server.sh` starts
and unseals file-backed test servers inside that container. It preserves data
across layout changes; it never manages host containers or cluster resources.
The operator actions and assertions are in
`tests/bdd/features/openbao-jwt-plugin-lifecycle.feature`.

`runtime-raft.sh` prepares three isolated Raft servers and replaces standbys
before the former active. `runtime-raft-bao.sh` selects one server and its
private test credential. Keep assertions in
`tests/bdd/features/openbao-raft-leadership.feature`, including local leader
identity, unchanged voters, plugin versions and signatures after each tune.
