// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/journal"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/telemetry"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

// The suppressed line and its stub are the first case of
// transform/suppress/testdata/python.json, which the earlier shell
// exporter's Python step produced byte for byte.
const (
	suppressedLine = `{"timestamp":101,"event":{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":1777312801000,"event_source":"frontend","payload":{"request_id":"r1","endpoint":"chat","model":"m","request":{"messages":[{"role":"user","content":"secret"}]},"response":{"choices":[]},"http_request_headers":{"nvcf-ncaid":"nca-acme-nca"},"payload_complete":true}}}`
	shellStub      = `{"timestamp":101,"event":{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":1777312801000,"event_source":"frontend","payload":{"request_id":"r1","endpoint":"chat","model":"m","payload_complete":false,"payload_drop_reason":"nca_id","payload_suppressed":true,"suppression_reason":"nca_id","nca_id":"acme","http_request_headers":{"nvcf-ncaid":"acme"}}}}`
)

func TestChartSuppressionEnvExportsTheShellStub(t *testing.T) {
	keptPayload := uploadertest.RequestPayload("r2", 1777312802000, map[string]string{"nvcf-ncaid": "nca-beta-nca"})
	keptEnd := uploadertest.RequestEnd("r1", 1777312803000)
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	closed := spool.Roll(uploadertest.NewSegment().Member(suppressedLine, keptPayload, keptEnd).Bytes())
	active := spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("r3", 1)).Bytes())

	// The chart renders suppressedNcaIds with `| quote`, so the container
	// sees the comma-separated list as it was written in the values.
	cfg := loadEnv(t, spoolEnv(t, spool.Dir, map[string]string{
		config.EnvSuppressNCAIDs: "nca-acme-nca,other-org",
	}))
	exporter := newCapture(t, backendtest.NewFake())
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	uploader, err := service.NewUploader(cfg, exporter, service.Options{Metrics: metrics})
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}

	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}

	exported := exporter.exported()
	if want := []string{shellStub, keptPayload, keptEnd}; !slices.Equal(exported, want) {
		t.Fatalf("exported =\n%s\nwant\n%s", strings.Join(exported, "\n"), strings.Join(want, "\n"))
	}
	if strings.Contains(strings.Join(exported, "\n"), "secret") {
		t.Fatal("the suppressed request body was exported")
	}
	for name, labels := range map[string]telemetry.Labels{
		telemetry.RecordsSuppressed:        {"rule": config.NCAIDRuleName, "action": "strip_payload"},
		telemetry.SuppressionHeaderPresent: {"rule": config.NCAIDRuleName},
	} {
		want := map[string]float64{telemetry.RecordsSuppressed: 1, telemetry.SuppressionHeaderPresent: 2}[name]
		if got, _ := metrics.Value(name, labels); got != want {
			t.Errorf("%s%v = %v, want %v", name, labels, got, want)
		}
	}
	if got, _ := metrics.Value(telemetry.RecordsEvaluated, telemetry.Labels{"record_type": "request_payload"}); got != 2 {
		t.Errorf("records_evaluated = %v, want 2", got)
	}
	if _, err := os.Stat(closed); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("closed segment after a confirmed export: %v, want removed", err)
	}
	if _, err := os.Stat(active); err != nil {
		t.Errorf("active segment: %v, want it left for the writer", err)
	}
}

func TestSuppressionCountersArePresetAndCarryOnlyRuleNames(t *testing.T) {
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	cfg := loadEnv(t, spoolEnv(t, spool.Dir, map[string]string{
		config.EnvSuppressNCAIDs:   "acme",
		config.EnvSuppressionRules: `[{"name":"tenants","headers":["x-tenant"],"values":["t1"],"action":"dropRecord"}]`,
	}))
	metrics, err := telemetry.NewMemory("fake")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.NewUploader(cfg, backendtest.NewFake(), service.Options{Metrics: metrics}); err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}

	for _, want := range []struct {
		name   string
		labels telemetry.Labels
	}{
		{telemetry.RecordsSuppressed, telemetry.Labels{"rule": "nca_id", "action": "strip_payload"}},
		{telemetry.RecordsSuppressed, telemetry.Labels{"rule": "tenants", "action": "drop_record"}},
		{telemetry.SuppressionHeaderPresent, telemetry.Labels{"rule": "nca_id"}},
		{telemetry.SuppressionHeaderPresent, telemetry.Labels{"rule": "tenants"}},
	} {
		if value, ok := metrics.Value(want.name, want.labels); !ok || value != 0 {
			t.Errorf("%s%v = %v, %v; want a zero series before any record", want.name, want.labels, value, ok)
		}
	}
	for _, name := range []string{telemetry.RecordsSuppressed, telemetry.SuppressionHeaderPresent} {
		for _, labels := range metrics.Series(name) {
			for key, value := range labels {
				if strings.Contains(value, "ncaid") || strings.Contains(value, "tenant-") || value == "acme" || value == "t1" || value == "x-tenant" {
					t.Errorf("%s label %s=%q exposes a header name or value", name, key, value)
				}
			}
		}
	}
}

func TestWarnsOnceWhenNoSuppressionHeaderIsSeen(t *testing.T) {
	logs := captureLogs(t)
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	cfg := loadEnv(t, spoolEnv(t, spool.Dir, map[string]string{config.EnvSuppressNCAIDs: "acme"}))
	exporter := newCapture(t, backendtest.NewFake())
	uploader, err := service.NewUploader(cfg, exporter, service.Options{HeaderCheckRecords: 3})
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}
	headerless := func(id string) string { return uploadertest.RequestPayload(id, 1, nil) }
	const warning = "no payload record carried a suppression header"

	spool.Roll(uploadertest.NewSegment().Member(headerless("a"), headerless("b")).Bytes())
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("x", 1)).Bytes())
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), warning) {
		t.Fatalf("warned after 2 payload records, want a warning only after 3:\n%s", logs)
	}

	for range 2 {
		spool.Roll(uploadertest.NewSegment().Member(headerless("c"), headerless("d")).Bytes())
		if err := uploader.Pass(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Count(logs.String(), warning); got != 1 {
		t.Fatalf("warnings = %d, want exactly 1:\n%s", got, logs)
	}
	// The condition only warns: every record of the closed segments (a, b,
	// x, c, d) is still exported.
	if got := len(exporter.exported()); got != 5 {
		t.Fatalf("exported %d records, want all 5", got)
	}
}

func TestNoHeaderWarningOnceAHeaderIsSeen(t *testing.T) {
	logs := captureLogs(t)
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	cfg := loadEnv(t, spoolEnv(t, spool.Dir, map[string]string{config.EnvSuppressNCAIDs: "acme"}))
	uploader, err := service.NewUploader(cfg, backendtest.NewFake(), service.Options{HeaderCheckRecords: 2})
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}
	spool.Roll(uploadertest.NewSegment().Member(
		uploadertest.RequestPayload("a", 1, map[string]string{"nvcf-ncaid": "other"}),
		uploadertest.RequestPayload("b", 1, nil),
		uploadertest.RequestPayload("c", 1, nil),
	).Bytes())
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("x", 1)).Bytes())
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs.String(), "suppression header") {
		t.Fatalf("warned although a header was seen:\n%s", logs)
	}
}

func TestATransportPreparedUnderAnotherPolicyIsPreparedAgainAfterARestart(t *testing.T) {
	if uploadertest.CrashChild() {
		t.Skip("runs only in the parent")
	}
	landing := t.TempDir()
	spool, closed := spoolWith(t, []string{suppressedLine})
	// The child prepares with suppression off and is killed before it
	// submits.
	result := uploadertest.RunCrash(t, "TestCrashChildRunsThePipeline", service.PointPrepared,
		envCrashSpool+"="+spool.Dir, envCrashLanding+"="+landing)
	if !result.Killed || result.Point != service.PointPrepared {
		t.Fatalf("child was not killed at %s:\n%s", service.PointPrepared, result.Output)
	}

	logs := captureLogs(t)
	restarted := newUploader(t, spool, dirBackend{root: landing}, service.Options{}, map[string]string{config.EnvSuppressNCAIDs: "nca-acme-nca"})
	passUntil(t, restarted, func() bool { return gone(closed[0]) })
	files, err := filepath.Glob(filepath.Join(landing, "*", "*.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	var exported []string
	for _, file := range files {
		exported = append(exported, readGzipLines(t, file)...)
	}
	joined := strings.Join(exported, "\n")
	if strings.Contains(joined, "secret") || !strings.Contains(joined, `"payload_suppressed":true`) {
		t.Fatalf("exported = %s, want the record suppressed under the policy now configured", joined)
	}
	if strings.Contains(logs.String(), "nca-acme-nca") {
		t.Fatalf("logs carry a configured suppression value:\n%s", logs)
	}
}

// refusingBackend refuses every submit in a way that shows nothing was
// processed, as a 503 does.
type refusingBackend struct{ dirBackend }

func (refusingBackend) Submit(context.Context, backend.SubmitRequest) (string, error) {
	return "", &backend.Fault{Class: backend.ClassRetryable, Scope: backend.ScopeUnknown, Reason: backend.ReasonUnavailable, Err: errors.New("unavailable")}
}

func TestARefusedBatchIsPreparedAgainWhenThePolicyChangesBeforeItsRetry(t *testing.T) {
	landing := t.TempDir()
	spool, closed := spoolWith(t, []string{suppressedLine})
	first := newUploader(t, spool, refusingBackend{dirBackend{root: landing}}, service.Options{}, nil)
	_ = first.Pass(context.Background())
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	batches := journalBatches(t, spool)
	if len(batches) != 1 || batches[0].State != journal.StateRetryWait {
		t.Fatalf("batches = %+v, want one refused batch waiting to retry", batches)
	}

	restarted := newUploader(t, spool, dirBackend{root: landing}, service.Options{}, map[string]string{config.EnvSuppressNCAIDs: "nca-acme-nca"})
	passUntil(t, restarted, func() bool { return gone(closed[0]) })
	files, err := filepath.Glob(filepath.Join(landing, "*", "*.jsonl.gz"))
	if err != nil {
		t.Fatal(err)
	}
	var exported []string
	for _, file := range files {
		exported = append(exported, readGzipLines(t, file)...)
	}
	joined := strings.Join(exported, "\n")
	if strings.Contains(joined, "secret") || !strings.Contains(joined, `"payload_suppressed":true`) {
		t.Fatalf("exported = %s, want the record suppressed under the policy now configured", joined)
	}
}
