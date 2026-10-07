# LLM routing stack

This directory installs model-neutral LLM routing infrastructure and independent model recipes. Read `README.md`, `ADVANCED.md` and `recipes/AGENTS.md`. Keep the shared Helm installation and `llm.py` access commands in `README.md`. Keep configuration, individual phases, existing Python workflows and recovery in `ADVANCED.md`.

`charts/shared-stack` owns the shared Helm lifecycle and post-install/post-upgrade gateway verification. Keep TLS, model discovery and caller-key authentication checks in the chart. Model charts own their preparation, cache, serving and endpoint resources. Keep GPU, model pins, RuntimeClass and model storage requirements in recipe-owned metadata. `llm.py` supplies model discovery and chat using the current kube context. Keep its credentials and port forwards scoped to each command. `stack.py` supplies the existing verified-connection and development image workflow. Preserve its supported commands when extending Helm installation.

Edit runtime code in its owning source directories in the current checkout. Builds include local edits. Commit IDs are informational. Python image updates compare routing chart contents with the fingerprint recorded in the installed stack.

Run `python3 -m unittest discover -s tests -v` for shared infrastructure, plus the recipe tests and offline Helm renders documented in `ADVANCED.md`. Run `python3 recipes/export_catalog.py --check` after recipe or chart metadata changes. Package charts with `bash package-charts.sh --output-dir /path/to/fresh-output` after preparing local dependency archives. Keep package outputs outside the checkout.

Pass an explicit Kubernetes context on cluster operations. Packaging tests must preserve live deployments. Helm rendering and local tests establish packaging behavior. Fresh-cluster installation, model readiness and inference require separate hardware evidence.

Keep mocks under test directories and credentials, private targets, kubeconfigs and evidence in an external work directory. Publish only public example values.
