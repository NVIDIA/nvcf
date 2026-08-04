# JWT Plugin Provenance

The committed `vault-plugin-secrets-jwt` binaries are rebuilt from NVIDIA's
internal fork:

- Source: https://gitlab-master.nvidia.com/kaizen-data/forks/vault-plugin-secrets-jwt
- Commit: `183b3159512f6fcfe766c8a3d738f47a751bad5c`
- Build script: `scripts/build-jwt-plugin.sh`
- Verification script: `scripts/verify-jwt-plugin.sh`

Pinned dependency floor for `NVCF-10946`:

- `golang.org/x/net v0.55.0`

Compatibility pins retained from the previously shipped NVCF plugin binary:

- `github.com/hashicorp/vault/api v1.15.0`
- `github.com/hashicorp/vault/sdk v0.15.2`
- `google.golang.org/grpc v1.69.4`
- `github.com/go-jose/go-jose/v4 v4.0.4`

The OpenBao producer image copies one binary per target platform into
`/openbao/plugins/vault-plugin-secrets-jwt`. Run
`scripts/verify-jwt-plugin.sh` before publishing the image.

Current committed binary hashes:

- `vault-plugin-secrets-jwt-linux-amd64`: `be2a2bcea1e028c6a6be43877facafd12509c07aa09ce2da982fa9117135d006`
- `vault-plugin-secrets-jwt-linux-arm64`: `88a14ef10d3fc1a6290ffc78de3367de92de7cb56e9d45a097c7c945f13ec77d`
