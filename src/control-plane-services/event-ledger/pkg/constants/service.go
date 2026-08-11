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

package constants

const ApiSvcName = "event-ledger-api"
const SvcName = "event-ledger"

var ValidEvents = map[string]interface{}{
	"pending":                    nil,
	"pendingError":               nil,
	"building":                   nil,
	"buildingError":              nil,
	"downloadingModel":           nil,
	"downloadingModelError":      nil,
	"downloadingContainer":       nil,
	"downloadingContainerError":  nil,
	"initializingContainer":      nil,
	"initializingContainerError": nil,
	"ready":                      nil,
	// active: sent from NVCF-API when a function switches to active state
	// "active":                nil,
	"requestingTermination": nil,
	"destroyed":             nil,
}

var ValidStages = map[string][]string{
	"error": {
		"pendingError",
		"buildingError",
		"downloadingModelError",
		"downloadingContainerError",
		"initializingContainerError",
	},
	"pending": {
		"pending",
		"building",
		"downloadingModel",
		"downloadingContainer",
		"initializingContainer",
	},
	"ready": {
		"ready",
		// "active",
	},
	"destroyed": {
		"destroyed",
		"requestingTermination",
	},
}
