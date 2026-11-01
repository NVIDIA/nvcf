package main

import "testing"

func TestNormaliseActualIgnoresConditionalConfigMapMetric(t *testing.T) {
	actual := goldenData{Result: []goldenMetric{
		{
			Metric: map[string]string{
				"__name__":  "kube_configmap_created",
				"configmap": "nvcf-miniservice-metadata",
			},
		},
		{
			Metric: map[string]string{"__name__": "byoo_request_total"},
		},
	}}

	normaliseActual(&actual)

	if len(actual.Result) != 1 {
		t.Fatalf("got %d metrics after normalisation, want 1", len(actual.Result))
	}
	if got := actual.Result[0].Metric["__name__"]; got != "byoo_request_total" {
		t.Errorf("retained metric = %q, want byoo_request_total", got)
	}
}
