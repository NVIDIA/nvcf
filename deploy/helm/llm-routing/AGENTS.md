# LLM routing stack

This directory deploys the LLM routing stack and a model recipe on ARM64 NVIDIA GPU clusters, such as DGX Spark and GB300. Read `README.md` and `recipes/AGENTS.md`. Edit runtime code in its owning source directories in the current checkout. Builds include local edits. Commit IDs are informational. Image updates compare routing chart contents with the fingerprint recorded in the installed stack.

Run the Python tests and offline Helm render documented in the README. Always specify the Kubernetes context. Packaging validation must not change a live model deployment.

Keep mocks under `recipes/tests` and out of the default deployment. Keep credentials, private targets, kubeconfigs and evidence in an external work directory. Do not publish private configuration overlays.
