# LLM routing stack

This directory deploys the LLM routing stack and GLM on DGX Spark. Read `README.md` and `spark/AGENTS.md`. Edit runtime code in its owning source directories in the current checkout. `spark/source.lock.json` records the required compatibility baseline, which must be an ancestor of the checkout's `HEAD`. Builds include local edits.

Run the Python tests and offline Helm render documented in the README. Always specify the Kubernetes context. Packaging validation must not change a live model deployment.

Keep mocks under `spark/tests` and out of the default deployment. Keep credentials, private targets, kubeconfigs and evidence in an external work directory. Do not publish private configuration overlays.
