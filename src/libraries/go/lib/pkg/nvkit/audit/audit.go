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

package audit

import (
	"time"
)

// AuditRecorder describes the functionality that is required to be write/record an audit-event
type AuditRecorder interface {
	RecordEvent(*AuditPayload)
}

// Auditor captures the various functionality of the any audit provider
type Auditor interface {
	AuditRecorder
}

// AuditConfig captures the various config necessary for add audit functionality to a component
type AuditConfig struct {
	HMACKey []byte
}

// AuditLog captures the format of the audit event
type AuditLog struct {
	Tenant  string       `json:"tenant"`
	Payload AuditPayload `json:"payload"`
	Hmac    string       `json:"hmac"`
}

// AuditPayload contains the actual information provided by the component generting the audit event
type AuditPayload struct {
	Timestamp       time.Time        `json:"timestamp"`
	ParentAuditID   string           `json:"parentAuditId"`
	ID              string           `json:"id"`
	MachineID       string           `json:"machineId"`
	Operation       string           `json:"operation"`
	Type            string           `json:"type"`
	ActorID         string           `json:"actorId"`
	ActorLocation   string           `json:"actorLocation"`
	SubjectID       string           `json:"subjectId"`
	SubjectLocation string           `json:"subjectLocation"`
	ObjectID        string           `json:"objectId"`
	ObjectLocation  string           `json:"objectLocation"`
	HmacBefore      string           `json:"hmacBefore"`
	HmacAfter       string           `json:"hmacAfter"`
	StateSummary    string           `json:"stateSummary"`
	Summary         string           `json:"summary"`
	State           string           `json:"state"`
	Data            AuditRequestData `json:"data"`
}

// AuditRequestData captures the core information about the request generating the audit event
type AuditRequestData struct {
	ErrorMessage string `json:"errorMessage"`
	HttpMethod   string `json:"httpMethod"`
	RequestUri   string `json:"requestUri"`
	RemoteAddr   string `json:"remoteAddr"`
	UserAgent    string `json:"userAgent"`
	HttpStatus   int    `json:"httpStatus"`
}
