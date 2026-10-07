# Shared stack tests

Run `python3 -m unittest discover -s deploy/helm/llm-routing/tests -v` from the repository root. Helm render tests use temporary copies of the local charts and skip when Helm is unavailable.

Keep cluster and gateway fixtures in these tests. Do not access a live cluster, mutate shared resources, or import the GLM installer. Cover ownership, identity, namespace isolation and model-free bootstrap behavior.
