# Demo monitoring chart

This chart owns the demo collector, VictoriaMetrics, Grafana, provisioning and pod-discovery permissions. Keep application metrics in their owning services. `values.yaml` uses JSON syntax so Helm and the standard-library Python runner share one set of image pins.

Run from `deploy/helm/llm-routing/recipes`:

```bash
python3 -m pip install -r tests/requirements-monitoring.txt
python3 -m unittest discover -s tests -p test_monitoring.py -v
helm lint --strict charts/monitoring
```

Verify collector configuration with the pinned image's `validate --config` command. Keep the dashboard JSON provisioned from `files/dashboard.json`. Missing or stale data must remain distinguishable from healthy samples. Keep discovery namespace-scoped and monitor each selected pod once.

Grafana credentials and generated values belong outside the checkout. `monitoring.py` reads the Helm-installed shared stack and supplies chart values, dashboard access and verification. Install and uninstall monitoring with Helm. Update the recipe NOTICE when changing external image pins. Grafana OSS's AGPL-3.0 license is outside the repository allowlist and must remain visible in dependency review.
