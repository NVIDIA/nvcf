# Demo monitoring chart

This chart owns the demo collector, VictoriaMetrics, Grafana, provisioning and pod-discovery permissions. Keep application metrics in their owning services. `values.yaml` uses JSON syntax so Helm and the standard-library Python runner share one set of image pins.

Run from `deploy/helm/llm-routing/spark`:

```bash
python3 -m pip install -r tests/requirements-monitoring.txt
python3 -m unittest discover -s tests -p test_monitoring.py -v
python3 spark.py --config config.example.json --work-dir /tmp/monitoring-render render
```

Verify collector configuration with the pinned image's `validate --config` command. Keep the dashboard JSON provisioned from `files/dashboard.json`. Missing or stale data must remain distinguishable from healthy samples. Keep discovery namespace-scoped and monitor each selected pod once.

Grafana credentials and generated values belong in the external work directory. Update the recipe NOTICE when changing external image pins. Grafana OSS's AGPL-3.0 license is outside the repository allowlist and must remain visible in dependency review.
