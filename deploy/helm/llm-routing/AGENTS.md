# Standalone routing POC

This chart composes the existing gateway and request-router charts. Keep runtime behavior changes in the owning service subtree. Test fixtures are not production authorizers or model recipes.

Run `bash tests/render.sh` from this directory. Run `bash tests/smoke.sh CONTEXT` against an installed CPU POC. Always specify the Kubernetes context.

Build `tests/fixture.go` from `src/invocation-plane-services/llm-api-gateway` with the existing Go module. Do not add dependencies for the fixture. Keep registry credentials and private image locations in external values files.
