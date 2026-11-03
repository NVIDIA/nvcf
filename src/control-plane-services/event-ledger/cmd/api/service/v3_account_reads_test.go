/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/config"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/data_access"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/internal/observability/logging"
	"github.com/NVIDIA/nvcf/src/control-plane-services/event-ledger/pkg/testutils"
)

const (
	accountTestNca      = "acct-1"
	accountTestFunction = "11111111-1111-1111-1111-111111111111"
	accountTestVersion  = "22222222-2222-2222-2222-222222222222"
)

type accountRow struct {
	namespace  string
	context    string
	ncaID      string
	functionID string
}

// accountReadsDB emulates the CQL filter: it returns a row only when every
// column matches, so rows with empty columns (legacy rows) never match.
type accountReadsDB struct {
	*mockDBHandlerV3
	eventRows  map[accountRow][]data_access.EventV3Record
	statsRows  map[accountRow][]data_access.StatsV3Record
	err        error
	lastEvents accountRow
	lastStats  accountRow
}

func (d *accountReadsDB) GetAccountEventsV3(_ context.Context, namespace, eventContext, ncaID, functionID string) ([]data_access.EventV3Record, error) {
	d.lastEvents = accountRow{namespace, eventContext, ncaID, functionID}
	if d.err != nil {
		return nil, d.err
	}
	return d.eventRows[d.lastEvents], nil
}

func (d *accountReadsDB) GetAccountStatsV3(_ context.Context, namespace, ncaID, functionID string) ([]data_access.StatsV3Record, error) {
	d.lastStats = accountRow{namespace: namespace, ncaID: ncaID, functionID: functionID}
	if d.err != nil {
		return nil, d.err
	}
	return d.statsRows[d.lastStats], nil
}

func newAccountReadsDB(t *testing.T) *accountReadsDB {
	t.Helper()
	now := time.Now()
	details := func(attrs map[string]any) json.RawMessage {
		raw, err := json.Marshal(EventDetails{Body: "body", Attributes: attrs})
		require.NoError(t, err)
		return raw
	}
	match := accountRow{accountTestVersion, "instance_id=pod-1", accountTestNca, accountTestFunction}
	statsMatch := accountRow{namespace: accountTestVersion, ncaID: accountTestNca, functionID: accountTestFunction}
	return &accountReadsDB{
		mockDBHandlerV3: &mockDBHandlerV3{},
		eventRows: map[accountRow][]data_access.EventV3Record{
			match: {
				{EventName: "pending", Source: "src", Details: details(map[string]any{"request": "r1"}), Timestamp: now.Add(-time.Minute)},
				{EventName: "ready", Source: "src", Details: details(map[string]any{"request": "r2"}), Timestamp: now},
			},
		},
		statsRows: map[accountRow][]data_access.StatsV3Record{
			statsMatch: {
				{Context: "pod-1", EventName: "pending", Timestamp: now},
				{Context: "pod-2", EventName: "ready", Timestamp: now},
				{Context: "pod-3", EventName: "ready", Timestamp: now},
			},
		},
	}
}

func serveAccountRead(t *testing.T, db data_access.DBHandlerV2, route, query string, vars map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	logger := testutils.InitTestLogger(t)
	server := NewServer(Connections{DbHandlerV2: db}, logger, nil, "test", &config.HTTPClientConfig{},
		config.PaginationConfig{}, config.StatsConfig{})

	req := httptest.NewRequest(http.MethodGet, "/"+route+query, nil)
	ctx := context.WithValue(req.Context(), logging.LoggerKey, logging.NewTraceLogger(req.Context(), logger))
	req = mux.SetURLVars(req.WithContext(ctx), vars)

	w := httptest.NewRecorder()
	switch route {
	case "events":
		server.GetAccountEventsV3(w, req)
	case "stats":
		server.GetAccountStatsV3(w, req)
	default:
		t.Fatalf("unknown route %q", route)
	}
	return w
}

func accountVars(nca, function, version string) map[string]string {
	return map[string]string{"ncaId": nca, "functionId": function, "functionVersionId": version}
}

func TestGetAccountEventsV3(t *testing.T) {
	const podQuery = "?instance_id=pod-1"

	tests := []struct {
		name       string
		query      string
		vars       map[string]string
		wantStatus int
		wantEvents []string
	}{
		{"match returns rows newest first", podQuery, accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, []string{"ready", "pending"}},
		{"wrong account returns empty 200", podQuery, accountVars("acct-2", accountTestFunction, accountTestVersion), http.StatusOK, nil},
		{"wrong function returns empty 200", podQuery, accountVars(accountTestNca, "33333333-3333-3333-3333-333333333333", accountTestVersion), http.StatusOK, nil},
		{"wrong version returns empty 200", podQuery, accountVars(accountTestNca, accountTestFunction, "33333333-3333-3333-3333-333333333333"), http.StatusOK, nil},
		{"other context returns empty 200", "?instance_id=pod-9", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, nil},
		{"attribute filter keeps matching event", podQuery + "&attribute_key=request&attribute_value=r2", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, []string{"ready"}},
		{"attribute filter with no match returns empty 200", podQuery + "&attribute_key=request&attribute_value=nope", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, nil},
		{"attribute key without value", podQuery + "&attribute_key=request", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusBadRequest, nil},
		{"invalid context field", "?instance_id=bad%20value", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusBadRequest, nil},
		{"malformed ncaId", podQuery, accountVars("acct.1", accountTestFunction, accountTestVersion), http.StatusBadRequest, nil},
		{"empty ncaId", podQuery, accountVars("", accountTestFunction, accountTestVersion), http.StatusBadRequest, nil},
		{"overlong ncaId", podQuery, accountVars(strings.Repeat("a", maxNcaIDLength+1), accountTestFunction, accountTestVersion), http.StatusBadRequest, nil},
		{"malformed functionId", podQuery, accountVars(accountTestNca, "not-a-uuid", accountTestVersion), http.StatusBadRequest, nil},
		{"malformed functionVersionId", podQuery, accountVars(accountTestNca, accountTestFunction, "not-a-uuid"), http.StatusBadRequest, nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := serveAccountRead(t, newAccountReadsDB(t), "events", tc.query, tc.vars)
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			if tc.wantStatus != http.StatusOK {
				assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
				return
			}

			var response EventsV3Response
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, tc.vars["functionVersionId"], response.Namespace)
			require.NotNil(t, response.Events)
			var got []string
			for _, event := range response.Events {
				got = append(got, event.EventName)
			}
			assert.Equal(t, tc.wantEvents, got)
		})
	}
}

func TestGetAccountEventsV3_PassesPathValuesToDB(t *testing.T) {
	db := newAccountReadsDB(t)
	w := serveAccountRead(t, db, "events", "?instance_id=pod-1",
		accountVars(accountTestNca, accountTestFunction, accountTestVersion))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, accountRow{accountTestVersion, "instance_id=pod-1", accountTestNca, accountTestFunction}, db.lastEvents)
}

func TestGetAccountEventsV3_ResponseOmitsNcaID(t *testing.T) {
	w := serveAccountRead(t, newAccountReadsDB(t), "events", "?instance_id=pod-1",
		accountVars(accountTestNca, accountTestFunction, accountTestVersion))
	require.Equal(t, http.StatusOK, w.Code)
	assert.NotContains(t, w.Body.String(), "nca_id")
	assert.NotContains(t, w.Body.String(), accountTestNca)
}

func TestGetAccountEventsV3_DBErrorReturns500(t *testing.T) {
	db := newAccountReadsDB(t)
	db.err = fmt.Errorf("cassandra unavailable")
	w := serveAccountRead(t, db, "events", "?instance_id=pod-1",
		accountVars(accountTestNca, accountTestFunction, accountTestVersion))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
	assert.NotContains(t, w.Body.String(), "cassandra unavailable")
}

func TestGetAccountStatsV3(t *testing.T) {
	otherFunction := "33333333-3333-3333-3333-333333333333"

	tests := []struct {
		name          string
		query         string
		vars          map[string]string
		wantStatus    int
		wantTotal     int
		wantByEvent   map[string]int
		wantContextsN int
	}{
		{"match counts only returned rows", "", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, 3, map[string]int{"pending": 1, "ready": 2}, 3},
		{"event filter narrows contexts and summary", "?eventFilter=ready", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, 2, map[string]int{"ready": 2}, 2},
		{"event filter trims names", "?eventFilter=pending,%20ready", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, 3, map[string]int{"pending": 1, "ready": 2}, 3},
		{"view parameter is ignored", "?view=bogus", accountVars(accountTestNca, accountTestFunction, accountTestVersion), http.StatusOK, 3, map[string]int{"pending": 1, "ready": 2}, 3},
		{"wrong account returns empty 200", "", accountVars("acct-2", accountTestFunction, accountTestVersion), http.StatusOK, 0, map[string]int{}, 0},
		{"wrong function returns empty 200", "", accountVars(accountTestNca, otherFunction, accountTestVersion), http.StatusOK, 0, map[string]int{}, 0},
		{"malformed ncaId", "", accountVars("acct 1", accountTestFunction, accountTestVersion), http.StatusBadRequest, 0, nil, 0},
		{"malformed functionId", "", accountVars(accountTestNca, "nope", accountTestVersion), http.StatusBadRequest, 0, nil, 0},
		{"malformed functionVersionId", "", accountVars(accountTestNca, accountTestFunction, "nope"), http.StatusBadRequest, 0, nil, 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := serveAccountRead(t, newAccountReadsDB(t), "stats", tc.query, tc.vars)
			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			if tc.wantStatus != http.StatusOK {
				assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
				return
			}

			var response StatsV3Response
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			assert.Equal(t, accountTestVersion, response.Namespace)
			require.NotNil(t, response.Contexts)
			require.NotNil(t, response.Summary.ByEvent)
			assert.Len(t, response.Contexts, tc.wantContextsN)
			assert.Equal(t, tc.wantTotal, response.Summary.TotalContexts)
			assert.Equal(t, tc.wantByEvent, response.Summary.ByEvent)
		})
	}
}

func TestGetAccountStatsV3_PassesPathValuesToDB(t *testing.T) {
	db := newAccountReadsDB(t)
	w := serveAccountRead(t, db, "stats", "", accountVars(accountTestNca, accountTestFunction, accountTestVersion))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, accountRow{namespace: accountTestVersion, ncaID: accountTestNca, functionID: accountTestFunction}, db.lastStats)
}

func TestGetAccountStatsV3_DBErrorReturns500(t *testing.T) {
	db := newAccountReadsDB(t)
	db.err = fmt.Errorf("cassandra unavailable")
	w := serveAccountRead(t, db, "stats", "", accountVars(accountTestNca, accountTestFunction, accountTestVersion))
	assert.Equal(t, http.StatusInternalServerError, w.Code)
	assert.Equal(t, "application/problem+json", w.Header().Get("Content-Type"))
}
