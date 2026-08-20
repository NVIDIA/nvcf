/*
SPDX-FileCopyrightText: Copyright (c) NVIDIA CORPORATION & AFFILIATES. All rights reserved.
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
package invocation

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

// The work queue is owned by the invocation service, which is written in
// another language, so these formats are duplicated rather than shared. If they
// drift the purge silently targets a subject nothing was ever published to and
// removes nothing, with no error to show for it. These cases pin the exact
// strings against the owning service's request_stream_name and request_subject.
func TestRequestWorkStreamAndSubjectMatchTheInvocationService(t *testing.T) {
	requestId := uuid.MustParse("11111111-2222-3333-4444-555555555555")
	region := "us-west-2"
	versionId := "66666666-7777-8888-9999-000000000000"

	assert.Equal(t, "rq_us-west-2_66666666-7777-8888-9999-000000000000",
		requestWorkStream(region, versionId))
	assert.Equal(t, "rq.us-west-2.66666666-7777-8888-9999-000000000000.11111111-2222-3333-4444-555555555555",
		requestWorkSubject(region, versionId, requestId))
}

// The subject has to fall inside the stream's own subject space, otherwise a
// filtered purge matches nothing.
func TestRequestWorkSubjectIsCoveredByTheStreamSubjectSpace(t *testing.T) {
	region := "eu-west-1"
	versionId := uuid.New().String()

	subject := requestWorkSubject(region, versionId, uuid.New())
	assert.Regexp(t, `^rq\.`+region+`\.`+versionId+`\.`, subject)
}
