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

package consumer

import (
	"sync"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/proto/nvcf"
)

type WorkRequest struct {
	jetstream.Msg
	RequestData pb.WorkerInvokeFunctionRequest
	Region      string

	releaseOnce sync.Once
	release     func()
}

func NewWorkRequest(msg jetstream.Msg, region string, release func()) (*WorkRequest, error) {
	w := WorkRequest{Msg: msg, Region: region, release: release}
	err := proto.Unmarshal(msg.Data(), &w.RequestData)
	if err != nil {
		release()
		return nil, err
	}
	return &w, nil
}

func (w *WorkRequest) Close() error {
	w.releaseOnce.Do(w.release)
	return nil
}
