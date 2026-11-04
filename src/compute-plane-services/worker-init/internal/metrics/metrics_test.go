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

import "testing"

func TestCountersRegistered(t *testing.T) {
	if WorkerInitThreadCounter == nil {
		t.Fatal("WorkerInitThreadCounter must not be nil")
	}
	if WorkerInitArtifactCounter == nil {
		t.Fatal("WorkerInitArtifactCounter must not be nil")
	}
	if WorkerInitArtifactCachedCounter == nil {
		t.Fatal("WorkerInitArtifactCachedCounter must not be nil")
	}
	if WorkerInitArtifactBytesCounter == nil {
		t.Fatal("WorkerInitArtifactBytesCounter must not be nil")
	}
	if WorkerInitArtifactDownloadFailCounter == nil {
		t.Fatal("WorkerInitArtifactDownloadFailCounter must not be nil")
	}
}
