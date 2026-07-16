# AGENTS.md - image-credential-helper

Native Go service for Kubernetes image pull credential management in NVCF
bring-your-own-compute clusters.

## Layout

- `cmd/image-credential-helper/`: main binary entrypoint
- `vendor/github.com/NVIDIA/nvcf/src/libraries/go/lib/pkg/imagecredential/`:
  vendored shared registry credential logic
- `examples/`: sample Kubernetes manifests
- `docker/`: local Dockerfile

Registry-provider logic is owned in the monorepo shared library at
`src/libraries/go/lib/pkg/imagecredential`. Make provider changes there first,
then update this subtree's module/vendor state.

## Build and Test

```bash
make build
make test
make lint
make vendor-update
```

Useful focused checks:

```bash
go test ./cmd/image-credential-helper
go test ./cmd/image-credential-helper -run Test_runGlobal
make shellcheck
```

CI subproject id: `image-credential-helper`. Native CI and release wiring live
in `tools/ci/subproject-validations.yaml`, an internal GitLab CI config not
present in this public snapshot.

## Local Gotchas

- Dependency changes require `make vendor-update`, then root license
  attribution refresh via `./tools/scripts/update-license`.
- AWS ECR private registries match `*.dkr.ecr.*.amazonaws.com`.
- AWS ECR Public uses `public.ecr.aws` and is treated as public.
- Volcengine regions are parsed from `*.cr.volces.com` hostnames.
- Pull secret names follow `workload-{id}-regcred-{index}` or
  `worker-{id}-regcred-{index}`.
- Never commit real registry credentials or kubeconfigs.
