// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/backend/backendtest"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/config"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/service"
	"github.com/NVIDIA/nvcf/src/compute-plane-services/request-trace-uploader/uploadertest"
)

var pngBase64 = base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7, 99, 201}, 60)...))

func mediaRecord() string {
	return `{"schema":"dynamo.request.trace.v1","event_type":"request_payload","event_time_unix_ms":2,"payload":{"request_id":"m","request":{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` +
		pngBase64 + `"}}]}]}}}`
}

// exportOnce runs one pass over a spool holding one closed segment of lines
// and returns what the backend received.
func exportOnce(t *testing.T, extra map[string]string, lines ...string) []string {
	t.Helper()
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	spool.Roll(uploadertest.NewSegment().Member(lines...).Bytes())
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("active", 1)).Bytes())
	exporter := newCapture(t, backendtest.NewFake())
	uploader, err := service.NewUploader(loadEnv(t, spoolEnv(t, spool.Dir, extra)), exporter, service.Options{})
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
	return exporter.exported()
}

func TestMediaIsRedactedByDefault(t *testing.T) {
	exported := exportOnce(t, nil, mediaRecord())
	if len(exported) != 1 || strings.Contains(exported[0], pngBase64) || !strings.Contains(exported[0], `"redacted":"multimodal"`) {
		t.Fatalf("exported = %v, want the media replaced", exported)
	}
}

func TestRedactionSettingOffExportsMediaUnchanged(t *testing.T) {
	exported := exportOnce(t, map[string]string{config.EnvRedactMultimodal: "false"}, mediaRecord())
	if want := []string{mediaRecord()}; !slices.Equal(exported, want) {
		t.Fatalf("exported = %v, want the record unchanged", exported)
	}
}

func TestAuditPrefixSegmentsAreExportedToo(t *testing.T) {
	spool := uploadertest.NewSpool(t, config.DefaultSegmentPrefix)
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("trace", 1)).Bytes())
	spool.Roll(uploadertest.NewSegment().Member(uploadertest.RequestEnd("trace-active", 1)).Bytes())
	audit := uploadertest.NewSpool(t, "request-audit")
	audit.Dir = spool.Dir
	audit.WriteAt(0, uploadertest.NewSegment().Member(uploadertest.RequestEnd("audit", 1)).Bytes())
	audit.WriteAt(1, uploadertest.NewSegment().Member(uploadertest.RequestEnd("audit-active", 1)).Bytes())

	exporter := newCapture(t, backendtest.NewFake())
	cfg := loadEnv(t, spoolEnv(t, spool.Dir, map[string]string{config.EnvAuditPrefix: "request-audit"}))
	uploader, err := service.NewUploader(cfg, exporter, service.Options{})
	if err != nil {
		t.Fatalf("NewUploader() error = %v", err)
	}
	if err := uploader.Pass(context.Background()); err != nil {
		t.Fatalf("Pass() error = %v", err)
	}
	got := strings.Join(exporter.exported(), "\n")
	for _, id := range []string{`"trace"`, `"audit"`} {
		if !strings.Contains(got, id) {
			t.Errorf("exported = %s, want the closed segment with request %s", got, id)
		}
	}
	if strings.Contains(got, "active") {
		t.Errorf("exported = %s, want no active segment", got)
	}
}
