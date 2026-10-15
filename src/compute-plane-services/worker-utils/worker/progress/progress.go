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

package progress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/goccy/go-json"
	"go.uber.org/zap"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/metering"
	nvcfMetrics "github.com/NVIDIA/nvcf/src/libraries/go/worker/metrics/nvcf"
)

type Progress struct {
	RequestId       string         `json:"id"`
	PercentComplete uint32         `json:"progress"`
	PartialResponse map[string]any `json:"partialResponse"`
}

type MonitoredWork struct {
	Context       context.Context
	MeteringEvent *metering.EventDetails
	RespondToWork chan<- *http.Response
	LastUpdate    time.Time
}

type Monitor struct {
	responseDir   string
	watcher       *fsnotify.Watcher
	monitoredWork map[string]*MonitoredWork
	lock          sync.RWMutex
	monitorClosed chan struct{}
}

func New(responseDir string) *Monitor {
	return &Monitor{
		responseDir:   responseDir,
		monitoredWork: make(map[string]*MonitoredWork),
		monitorClosed: make(chan struct{}),
	}
}

func (m *Monitor) Start() error {
	m.lock.Lock()
	defer m.lock.Unlock()

	if m.watcher != nil {
		return nil
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}

	err = watcher.Add(m.responseDir)
	if err != nil {
		return err
	}

	m.watcher = watcher
	go m.monitorProgress()
	return nil
}

func (m *Monitor) Stop() error {
	m.lock.Lock()
	defer m.lock.Unlock()

	if m.watcher == nil {
		return nil
	}

	err := m.watcher.Close()
	if err != nil {
		return err
	}
	<-m.monitorClosed

	m.watcher = nil
	m.monitoredWork = make(map[string]*MonitoredWork)

	return nil
}

func (m *Monitor) Monitor(requestId string, monitoredProgress *MonitoredWork) {
	m.lock.Lock()
	m.monitoredWork[requestId] = monitoredProgress
	m.lock.Unlock()
}

func (m *Monitor) Drop(requestId string) {
	m.lock.Lock()
	delete(m.monitoredWork, requestId)
	m.lock.Unlock()
}

func (m *Monitor) WatchGaugeCallback() float64 {
	if m.watcher == nil {
		return 0.0
	}

	return float64(len(m.watcher.WatchList()))
}

func (m *Monitor) monitorProgress() {
	for {
		select {
		case event, ok := <-m.watcher.Events:
			if !ok {
				// Events channel has closed indicating watcher has closed.
				m.monitorClosed <- struct{}{}
				return
			}

			// Start watching new directories that appear.
			// This library will stop watching them once they are removed.
			if event.Op == fsnotify.Create {
				f, err := os.Stat(event.Name)
				if err != nil {
					zap.L().Error("failed stat file", zap.Error(err))
					continue
				}
				if f.IsDir() {
					err = m.watcher.Add(event.Name)
					if err != nil {
						zap.L().Error("failed to add watch", zap.String("name", event.Name), zap.Error(err))
					}
					continue
				}
			}

			// We only care about creates/writes to the progress file at this point.
			if filepath.Base(event.Name) != "progress" || !(event.Op == fsnotify.Create || event.Op == fsnotify.Write) {
				continue
			}

			progressFile, err := os.Open(event.Name)
			if err != nil {
				zap.L().Error("failed to open progress file", zap.Error(err))
				continue
			}

			progressFileStat, err := progressFile.Stat()
			if err != nil {
				zap.L().Error("failed to stat progress file", zap.Error(err))
				continue
			}

			progressData, err := io.ReadAll(progressFile)
			_ = progressFile.Close()
			if err != nil {
				zap.L().Error("failed to read progress file", zap.Error(err))
				continue
			}

			var progress Progress
			err = json.Unmarshal(progressData, &progress)
			if err != nil {
				zap.L().Error("failed to parse progress file", zap.Error(err))
				continue
			}

			partialResponseBody, err := json.Marshal(progress.PartialResponse)
			if err != nil {
				zap.L().Error("failed to convert partial response body", zap.Error(err))
				continue
			}

			m.lock.RLock()
			monitoredWork, ok := m.monitoredWork[progress.RequestId]
			m.lock.RUnlock()
			if !ok {
				zap.L().Error("found unknown request id", zap.String("req id", progress.RequestId))
				continue
			}

			if !progressFileStat.ModTime().After(monitoredWork.LastUpdate) {
				continue
			}

			nvcfMetrics.ResponseBytesCounter.Add(float64(len(partialResponseBody)))
			monitoredWork.MeteringEvent.InferenceSize += int64(len(partialResponseBody))
			select {
			case <-monitoredWork.Context.Done():
				err = monitoredWork.Context.Err()
			case monitoredWork.RespondToWork <- &http.Response{
				StatusCode: http.StatusAccepted,
				Header: http.Header{
					"Content-Type":          []string{"application/json"},
					"Content-Length":        []string{strconv.Itoa(len(partialResponseBody))},
					"Nvcf-Percent-Complete": []string{strconv.Itoa(int(progress.PercentComplete))},
				},
				Body:          io.NopCloser(bytes.NewReader(partialResponseBody)),
				ContentLength: int64(len(partialResponseBody)),
			}:
				err = nil
			}
			if err != nil {
				zap.L().Error("failed to send progress message", zap.String("req id", progress.RequestId), zap.Error(err))
			}
			monitoredWork.LastUpdate = progressFileStat.ModTime()
		case err, ok := <-m.watcher.Errors:
			if !ok {
				// Errors channel has closed indicating watcher has closed.
				m.monitorClosed <- struct{}{}
				return
			}
			zap.L().Warn("progress watcher error", zap.Error(err))
		}
	}
}
