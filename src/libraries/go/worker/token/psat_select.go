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

package token

import (
	"errors"
	"fmt"

	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"github.com/NVIDIA/nvcf/src/libraries/go/worker/utils"
)

// TokenSourceSetter is satisfied by auth.SettableTokenSource.
type TokenSourceSetter interface {
	SetTokenSource(oauth2.TokenSource)
}

// SelectMountedToken installs a mounted projected ServiceAccount token on provider when one
// is present and reports whether it did. Only ErrNoMountedToken falls back to the bootstrap
// credential already held by provider; any other error is returned. Because the mounted
// token is a bearer credential, it may only be presented to an https endpoint.
func SelectMountedToken(provider TokenSourceSetter, apiFqdn string, service string) (bool, error) {
	mountedSrc, err := NewMountedJWTSource()
	if err != nil {
		if errors.Is(err, ErrNoMountedToken) {
			zap.L().Info("no mounted JWT; using legacy bootstrap credential (deprecated)", zap.String("service", service))
			return false, nil
		}
		return false, fmt.Errorf("mounted JWT unavailable: %w", err)
	}
	apiUrl, err := utils.PortSafeUrl(apiFqdn)
	if err != nil {
		return false, err
	}
	if apiUrl.Scheme != "https" {
		return false, fmt.Errorf("mounted JWT requires TLS but %s endpoint %q is not https", service, apiFqdn)
	}
	zap.L().Info("mounted JWT found; using projected ServiceAccount token as credential", zap.String("service", service))
	provider.SetTokenSource(mountedSrc)
	return true, nil
}
