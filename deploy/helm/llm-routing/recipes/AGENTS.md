# Independent model recipes

`catalog.json` owns supported model profiles, immutable artifact pins and workload envelopes. `recipes.py` plans placements from read-only Kubernetes inventory. The SGLang chart owns only a model release, its retained cache claims and its InferenceEndpoint. Keep shared gateway, operator and credentials out of this chart.

Node count comes from the selected eligible profile. Do not calculate tensor parallelism from weight size alone, allocate shared GPUs, or silently evict another workload. Keep deployment status as pending until real hardware verification passes. Keep environment capabilities, plans, credentials and evidence outside the checkout.

Run from this directory:

```bash
python3 -m pip install -r tests/requirements.txt
python3 -m unittest discover -s tests -v
```

Tests render every profile and phase. Live qualification, download, serving and failure tests are separate actions. Use an explicit Kubernetes context on every live command. Runtime or model pin changes require a new Spark validation pass.
