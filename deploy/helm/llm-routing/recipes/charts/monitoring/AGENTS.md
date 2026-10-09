# Demo monitoring chart

This chart owns the demo collector, VictoriaMetrics, Grafana, provisioning and pod-discovery permissions. Keep application metrics in their owning services. `values.yaml` contains the default scrape targets for the shared `llm-stack` release in namespace `llm-stack`. Keep selectors and ports covered against the rendered shared chart.

Run from `deploy/helm/llm-routing/recipes`:

```bash
python3 -m pip install -r tests/requirements-monitoring.txt
python3 -m unittest discover -s tests -p test_monitoring.py -v
helm lint --strict charts/monitoring --set-string grafana.rootURL=http://192.0.2.10/grafana/
```

Verify collector configuration with the pinned image's `validate --config` command. Keep the dashboard JSON provisioned from `files/dashboard.json`. Missing or stale data must remain distinguishable from healthy samples. Keep discovery namespace-scoped and monitor each selected pod once.

Grafana credentials and environment values belong outside the checkout. Install and uninstall monitoring with Helm. Ingress is enabled by default using the Grafana URL and Traefik settings in `values.yaml`. `tests/verify_monitoring.py` checks live scrapes, dashboard permissions and optional inference traffic against the installed Helm release. Update the recipe NOTICE when changing external image pins. Grafana OSS's AGPL-3.0 license is outside the repository allowlist and must remain visible in dependency review.
