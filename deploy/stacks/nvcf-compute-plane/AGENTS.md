# Compute Plane stack

Run `make -C deploy/stacks/nvcf-compute-plane test-local` from the repository root.
Keep Helmfile values and golden renders aligned. Shared image versions must
agree with the Self-Managed released inventory before umbrella assembly.
Run live BDD qualification against the assembled bundle after publication.
