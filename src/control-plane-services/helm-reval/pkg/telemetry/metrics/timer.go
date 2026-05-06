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

package metrics

import (
	"context"
	"time"
)

type RunTimer interface {
	RecordThreadStart()
	RecordThreadEnd()
	RecordHelmDownloadStart()
	RecordHelmDownloadEnd()
	RecordImageCheckStart()
	RecordImageCheckEnd()

	GetLocalThreadDuration() time.Duration
	GetHelmDownloadDuration() time.Duration
	GetImageCheckDuration() time.Duration
}

func NewRunTimer() RunTimer {
	return &runTimer{now: time.Now}
}

type runTimerContextKey struct{}

func RunTimerIntoContext(parent context.Context, rt RunTimer) context.Context {
	return context.WithValue(parent, runTimerContextKey{}, rt)
}

func RunTimerFromContext(ctx context.Context) RunTimer {
	if rt, ok := ctx.Value(runTimerContextKey{}).(RunTimer); ok {
		return rt
	}
	return NewRunTimer()
}

type runTimer struct {
	now          func() time.Time
	thread       runTimes
	helmDownload runTimes
	imageCheck   runTimes
}

type runTimes struct {
	start, end time.Time
}

func (rt *runTimer) RecordThreadStart()       { rt.thread.start = rt.now() }
func (rt *runTimer) RecordThreadEnd()         { rt.thread.end = rt.now() }
func (rt *runTimer) RecordHelmDownloadStart() { rt.helmDownload.start = rt.now() }
func (rt *runTimer) RecordHelmDownloadEnd()   { rt.helmDownload.end = rt.now() }
func (rt *runTimer) RecordImageCheckStart()   { rt.imageCheck.start = rt.now() }
func (rt *runTimer) RecordImageCheckEnd()     { rt.imageCheck.end = rt.now() }

// getLocalThreadDuration returns only local (non-network-bound) thread runtime.
func (rt *runTimer) GetLocalThreadDuration() time.Duration {
	d := rt.thread.end.Sub(rt.thread.start) - rt.GetHelmDownloadDuration() - rt.GetImageCheckDuration()
	return max(d, 0)
}
func (rt *runTimer) GetHelmDownloadDuration() time.Duration {
	return rt.helmDownload.end.Sub(rt.helmDownload.start)
}
func (rt *runTimer) GetImageCheckDuration() time.Duration {
	return rt.imageCheck.end.Sub(rt.imageCheck.start)
}
