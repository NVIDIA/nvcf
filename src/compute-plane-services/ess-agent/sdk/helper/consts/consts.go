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

package consts

const (
	// ExpirationRestoreWorkerCount specifies the number of workers to use while
	// restoring leases into the expiration manager
	ExpirationRestoreWorkerCount = 64

	// NamespaceHeaderName is the header set to specify which namespace the
	// request is indented for.
	NamespaceHeaderName = "X-ESS-Namespace"

	// AuthHeaderName is the name of the header containing the token.
	AuthHeaderName = "X-ESS-Token"

	// RequestHeaderName is the name of the header used by the Agent for
	// SSRF protection.
	RequestHeaderName = "X-ESS-Request"

	// PerformanceReplicationALPN is the negotiated protocol used for
	// performance replication.
	PerformanceReplicationALPN = "replication_v1"

	// DRReplicationALPN is the negotiated protocol used for dr replication.
	DRReplicationALPN = "replication_dr_v1"

	PerfStandbyALPN = "perf_standby_v1"

	RequestForwardingALPN = "req_fw_sb-act_v1"

	RaftStorageALPN = "raft_storage_v1"

	// ReplicationResolverALPN is the negotiated protocol used for
	// resolving replicaiton addresses
	ReplicationResolverALPN = "replication_resolver_v1"

	VaultEnableFilePermissionsCheckEnv = "ESS_ENABLE_FILE_PERMISSIONS_CHECK"

	VaultDisableUserLockout = "ESS_DISABLE_USER_LOCKOUT"
)
