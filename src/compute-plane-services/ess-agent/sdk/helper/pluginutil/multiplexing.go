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

package pluginutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hashicorp/go-secure-stdlib/strutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var ErrNoMultiplexingIDFound = errors.New("no multiplexing ID found")

type PluginMultiplexingServerImpl struct {
	UnimplementedPluginMultiplexingServer

	Supported bool
}

func (pm PluginMultiplexingServerImpl) MultiplexingSupport(_ context.Context, _ *MultiplexingSupportRequest) (*MultiplexingSupportResponse, error) {
	return &MultiplexingSupportResponse{
		Supported: pm.Supported,
	}, nil
}

func MultiplexingSupported(ctx context.Context, cc grpc.ClientConnInterface, name string) (bool, error) {
	if cc == nil {
		return false, fmt.Errorf("client connection is nil")
	}

	out := strings.Split(os.Getenv(PluginMultiplexingOptOut), ",")
	if strutil.StrListContains(out, name) {
		return false, nil
	}

	req := new(MultiplexingSupportRequest)
	resp, err := NewPluginMultiplexingClient(cc).MultiplexingSupport(ctx, req)
	if err != nil {

		// If the server does not implement the multiplexing server then we can
		// assume it is not multiplexed
		if status.Code(err) == codes.Unimplemented {
			return false, nil
		}

		return false, err
	}
	if resp == nil {
		// Somehow got a nil response, assume not multiplexed
		return false, nil
	}

	return resp.Supported, nil
}

func GetMultiplexIDFromContext(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", fmt.Errorf("missing plugin multiplexing metadata")
	}

	multiplexIDs := md[MultiplexingCtxKey]
	if len(multiplexIDs) == 0 {
		return "", ErrNoMultiplexingIDFound
	} else if len(multiplexIDs) != 1 {
		return "", fmt.Errorf("unexpected number of IDs in metadata: (%d)", len(multiplexIDs))
	}

	multiplexID := multiplexIDs[0]
	if multiplexID == "" {
		return "", fmt.Errorf("empty multiplex ID in metadata")
	}

	return multiplexID, nil
}
