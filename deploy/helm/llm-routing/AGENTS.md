# LLM routing stack

This directory installs model-neutral LLM routing infrastructure and optional model recipes. Read `README.md`, then `spark/AGENTS.md` for GLM or `recipes/AGENTS.md` for catalog models. `stack.py` owns only shared infrastructure and must not require a GPU, model lock, RuntimeClass or model storage. Edit runtime code in its owning source directories in the current checkout. Builds include local edits. Commit IDs are informational. Image updates compare routing chart contents with the fingerprint recorded in the installed stack.

Run `python3 -m unittest discover -s tests -v` for the shared installer, plus the Python tests and offline Helm render documented in the README. Always specify the Kubernetes context. Packaging validation must not change a live model deployment.

Keep mocks under `spark/tests` and out of the default deployment. Keep credentials, private targets, kubeconfigs and evidence in an external work directory. Do not publish private configuration overlays.
