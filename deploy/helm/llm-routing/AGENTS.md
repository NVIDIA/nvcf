# LLM routing stack

This directory installs model-neutral LLM routing infrastructure and independent model recipes. Read `README.md`, `ADVANCED.md` and `recipes/AGENTS.md`. Keep the shared Helm installation and `llm.py` access commands in `README.md`. Keep configuration, upgrades, recovery and offline verification in `ADVANCED.md`. Keep k3s node preparation, its scripts and workstation kubeconfig setup in `cluster/`. Cluster scripts prepare nodes only and must not install or change `llm-stack`.

`charts/shared-stack` owns the shared Helm lifecycle and post-install/post-upgrade gateway verification. Keep TLS, model discovery and caller-key authentication checks in the chart. Model charts own their preparation, cache, serving and endpoint resources. Keep GPU, model pins, RuntimeClass and model storage requirements in recipe-owned metadata. `llm.py` supplies model discovery and chat using the current kube context. Keep its credentials and port forwards scoped to each command. The supported installation is release `llm-stack` in namespace `llm-stack`, with the operator and CRD included as a subchart. Helpers read the installed stack directly and must not require saved connection files or recreate a separate stack/operator installation path. Shared images and charts are published from release tags; see `ADVANCED.md`.

Edit runtime code in its owning source directories in the current checkout. Manage image changes through the shared Helm release and preserve installed credentials and TLS material.

Run `python3 -m unittest discover -s tests -v` for shared infrastructure, plus the recipe tests and offline Helm renders documented in `ADVANCED.md`. Run `python3 recipes/export_catalog.py --check` after recipe or chart metadata changes. Bump a chart's `Chart.yaml` version when its content changes. The catalog exports that version, so installs use it only after the chart is published at it.

Pass an explicit Kubernetes context on cluster operations. Helm rendering and local tests establish packaging behavior. Fresh-cluster installation, model readiness and inference require separate hardware evidence.

Keep mocks under test directories and credentials, private targets, kubeconfigs and evidence in an external work directory. Publish only public example values.
