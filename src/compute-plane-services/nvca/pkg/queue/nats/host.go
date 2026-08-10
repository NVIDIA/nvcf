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

package nats

import (
	"crypto/tls"
	"net/url"
	"strings"

	natsgo "github.com/nats-io/nats.go"
	log "github.com/sirupsen/logrus"
)

func natsHostOverrideOptions(natsURL, host string) []natsgo.Option {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	u, err := url.Parse(natsURLOrDefault(natsURL))
	if err != nil {
		log.WithError(err).Warnf("failed to parse NATS URL %q; skipping host override", natsURL)
		return nil
	}
	switch u.Scheme {
	case "tls", "wss":
		return []natsgo.Option{natsgo.Secure(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: host,
		})}
	default:
		return nil
	}
}
