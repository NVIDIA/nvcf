/*
SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
SPDX-License-Identifier: Apache-2.0
*/

package agent

import "testing"

// selfAgentURL decides what peers dial. Getting it wrong does not fail
// loudly -- it registers an unreachable address in the catalog and the
// cascade silently degrades to blobstore-only, so the fallback order is
// pinned here. See GH #490.
func TestSelfAgentURL(t *testing.T) {
	cases := []struct {
		name        string
		advertiseIP string
		nodeIP      string
		listen      string
		want        string
	}{
		{
			// Pod networking: peers must dial the pod IP; the node IP would
			// only reach us via hostPort.
			name: "advertise wins", advertiseIP: "10.1.2.3", nodeIP: "192.168.0.5",
			listen: ":8081", want: "http://10.1.2.3:8081",
		},
		{
			// hostNetwork: kubelet reports status.podIP as the node IP, so
			// both fields hold the same value and the URL is what it always
			// was. This is why enabling the new field changes nothing at the
			// default settings.
			name: "hostNetwork parity", advertiseIP: "192.168.0.5", nodeIP: "192.168.0.5",
			listen: ":8081", want: "http://192.168.0.5:8081",
		},
		{
			// An older deployment that sets only --node-ip keeps working.
			name: "falls back to node IP", advertiseIP: "", nodeIP: "192.168.0.5",
			listen: ":8081", want: "http://192.168.0.5:8081",
		},
		{
			// Neither known: return empty so the caller skips peer
			// registration rather than advertising a bogus endpoint.
			name: "no address", advertiseIP: "", nodeIP: "", listen: ":8081", want: "",
		},
		{
			name: "non-default port", advertiseIP: "10.1.2.3", nodeIP: "",
			listen: ":9090", want: "http://10.1.2.3:9090",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Agent{config: Config{
				AdvertiseIP: c.advertiseIP,
				NodeIP:      c.nodeIP,
				ListenAddr:  c.listen,
			}}
			if got := a.selfAgentURL(); got != c.want {
				t.Errorf("selfAgentURL() = %q, want %q", got, c.want)
			}
		})
	}
}
