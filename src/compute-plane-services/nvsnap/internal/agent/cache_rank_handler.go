/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package agent

import "net/http"

// cacheRankHandler streams a ready rank of a cache set from this node to
// the agent collecting the set.
func (a *Agent) cacheRankHandler(w http.ResponseWriter, r *http.Request) {
	if a.mvc == nil {
		http.Error(w, "model volume controller not running", http.StatusServiceUnavailable)
		return
	}
	a.mvc.ServeRank(w, r)
}
