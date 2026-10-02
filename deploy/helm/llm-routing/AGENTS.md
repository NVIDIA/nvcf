# LLM routing stack

This directory deploys the LLM routing stack and GLM on DGX Spark. Read `README.md` and `spark/AGENTS.md`. Runtime changes belong in the pinned owning source checkout, represented here by a reviewed patch and lock file.

Run the Python tests and offline Helm render documented in the README. Always specify the Kubernetes context. Packaging validation must not change a live model deployment.

Keep mocks under `spark/tests` and out of the default deployment. Keep credentials, private targets, kubeconfigs and evidence in an external work directory. Do not publish private configuration overlays.
