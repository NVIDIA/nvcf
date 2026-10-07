# LLM routing stack

This directory installs model-neutral LLM routing infrastructure and optional model recipes. Read `README.md`, `ADVANCED.md` and `recipes/AGENTS.md`. Keep the happy path in `README.md` and configuration, individual phases and recovery in `ADVANCED.md`. `stack.py` owns shared infrastructure. Keep GPU, model lock, RuntimeClass and model storage requirements in the recipes. Model recipes support ARM64 NVIDIA GPU clusters through their own runtime and placement rules, including DGX Spark and GB300.

Edit runtime code in its owning source directories in the current checkout. Builds include local edits. Commit IDs are informational. Image updates compare routing chart contents with the fingerprint recorded in the installed stack.

Run `python3 -m unittest discover -s tests -v` for the shared installer, plus the Python tests and offline Helm renders documented in `ADVANCED.md`. Always specify the Kubernetes context. Packaging validation must not change a live model deployment.

Keep mocks under `recipes/tests` and out of the default deployment. Keep credentials, private targets, kubeconfigs and evidence in an external work directory. Do not publish private configuration overlays.
