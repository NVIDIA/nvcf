# OpenBao source patches

Patches apply to the checksum-pinned upstream distribution before compilation.
Keep upstream MPL-2.0 headers and include focused Go regressions in each patch.
Record behavior changes in `../OPENBAO_PROVENANCE.md`.

Run `../scripts/build-openbao.sh` to apply patches, run their tests and verify
both server binaries. Run the live Raft BDD before changing transfer behavior.
