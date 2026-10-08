# Shared stack tests

Run `python3 -m unittest discover -s deploy/helm/llm-routing/tests -v` from the repository root. Helm render tests use temporary copies of the local charts and skip when Helm is unavailable.

Keep cluster and gateway fixtures in these tests. Do not access a live cluster, mutate shared resources, or introduce a separate Python installer. Cover release ownership, credential identity and model-free bootstrap behavior for `llm-stack` in namespace `llm-stack`.

Development image and package tests live in `../dev-artifacts/tests`. Keep this suite independent of temporary artifacts.
