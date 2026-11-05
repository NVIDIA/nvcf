# Self-Managed stack

Run `make -C deploy/stacks/self-managed test` from the repository root.
Use `tests/bdd` for live install and upgrade qualification after the selected
artifacts are published. Keep supported environment overrides and image
versions in the owning chart or stack defaults. Release inventories and
umbrella manifests must use immutable published artifacts.
