// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package agent

import (
	"context"
	"encoding/json"
	"maps"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/NVIDIA/nvcf/src/compute-plane-services/nvsnap/internal/fnstatus"
)

// A group record's key is scoped by the function version of the pods it is
// for (criuGroupScope), so a record belongs to one version. The agents name
// it on the record as labels (fnstatus.Identity), which nvsnap-server
// projects onto one NvsnapFunction per version
// (docs/design/FUNCTION-CACHE-STATUS.md). The labels are identity, never
// state: nothing nvsnap decides reads them.
//
// A record is named when it is created for a pod (a capture, a gpushare
// mark) and when admission looks it up, which also names records made
// before the labels existed.

// recordNamedTTL is how long an agent trusts that it named a record: one
// read per record per hour on admissions, not one per pod.
// recordNameRetry is how soon a failed attempt is tried again.
const (
	recordNamedTTL  = time.Hour
	recordNameRetry = 5 * time.Minute
	recordNameWait  = 10 * time.Second
)

var recordNamed sync.Map // record name -> time it was named, or a failed attempt's retry time

// nameRecordLater names group record key for namespace ns without holding
// up the caller: admission and capture do not wait on it.
func (a *Agent) nameRecordLater(key, ns string) {
	if a.kubeClient == nil || ns == "" {
		return
	}
	name := criuGroupConfigMapName(key)
	if !claimRecordName(name, time.Now()) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), recordNameWait)
		defer cancel()
		a.nameRecord(ctx, name, ns, true)
	}()
}

// claimRecordName reports whether record name is due to be named at now,
// and claims it so concurrent admissions name it once.
func claimRecordName(name string, now time.Time) bool {
	if v, ok := recordNamed.Load(name); ok && now.Before(v.(time.Time)) {
		return false
	}
	recordNamed.Store(name, now.Add(recordNamedTTL))
	return true
}

// nameRecord puts the identity of namespace ns on record name, and on the
// record it is an alias of when follow is set. A record that is gone is
// left alone; an attempt that fails is retried after recordNameRetry.
func (a *Agent) nameRecord(ctx context.Context, name, ns string, follow bool) {
	log := a.log.WithField("record", name).WithField("namespace", ns)
	retry := func(err error) {
		recordNamed.Store(name, time.Now().Add(recordNameRetry))
		log.WithError(err).Warn("function status: cannot name the record's function version")
	}
	nsLabels, err := a.namespaceLabels(ctx, ns)
	if err != nil {
		retry(err)
		return
	}
	want := fnstatus.Identity(ns, nsLabels, a.config.TenantLabel)
	cms := a.kubeClient.CoreV1().ConfigMaps(a.criuFailedNamespace())
	cm, err := cms.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return // gone; named again only after recordNamedTTL
	}
	if err != nil {
		retry(err)
		return
	}
	if follow && cm.Data["state"] == criuGroupStateAlias && cm.Data["alias"] != "" {
		a.nameRecord(ctx, criuGroupConfigMapName(cm.Data["alias"]), ns, false)
	}
	if named(cm.Labels, want) {
		return
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{"labels": want}})
	if err != nil {
		retry(err)
		return
	}
	if _, err := cms.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
		retry(err)
	}
}

// named reports whether labels hold every label of want.
func named(labels, want map[string]string) bool {
	have := make(map[string]string, len(labels)+len(want))
	maps.Copy(have, labels)
	maps.Copy(have, want)
	return maps.Equal(have, labels)
}
