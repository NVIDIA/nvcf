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

package models

import (
	"encoding/json"
	"fmt"
	"time"
)

// JWTClaims represents the decoded JWT payload
type JWTClaims struct {
	IAT    int64    `json:"iat"`
	EXP    int64    `json:"exp"`
	Scopes []string `json:"scopes"`
	Aud    []string `json:"-"` // Custom unmarshaling handles string or array
	Sub    string   `json:"sub,omitempty"`
}

// UnmarshalJSON implements custom JSON unmarshaling for JWTClaims
// to handle the 'aud' field which can be either a string or an array per RFC 7519
func (c *JWTClaims) UnmarshalJSON(data []byte) error {
	// Use an alias to avoid infinite recursion
	type Alias JWTClaims
	aux := &struct {
		Aud json.RawMessage `json:"aud,omitempty"`
		*Alias
	}{
		Alias: (*Alias)(c),
	}

	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Handle the 'aud' field: can be string or array
	if len(aux.Aud) > 0 {
		// Try to unmarshal as array first
		var audArray []string
		if err := json.Unmarshal(aux.Aud, &audArray); err == nil {
			c.Aud = audArray
		} else {
			// If array fails, try as a single string
			var audString string
			if err := json.Unmarshal(aux.Aud, &audString); err == nil {
				c.Aud = []string{audString}
			} else {
				return fmt.Errorf("aud field must be a string or array of strings")
			}
		}
	}

	return nil
}

// ServiceInfo represents service metadata from api-keys service
type ServiceInfo struct {
	ServiceID           string   `json:"service_id"`
	ServiceName         string   `json:"service_name"`
	AudienceServiceIDs  []string `json:"audience_service_ids"`
	MaxAPIKeysPerUser   int      `json:"max_api_keys_per_user"`
	MaxAPIKeyTTLDays    int      `json:"max_api_key_ttl_days"`
	MaxAuthzSizeChars   int      `json:"max_authz_size_chars"`
	MinAuthzUpdateIntervalSeconds int `json:"min_authz_update_interval_seconds"`
}

// ServicesResponse wraps the services array
type ServicesResponse struct {
	Services []ServiceInfo `json:"services"`
}

// Resource represents an authorization resource
type Resource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Policy represents an authorization policy
type Policy struct {
	Aud       string     `json:"aud"`
	Auds      []string   `json:"auds"`
	Product   string     `json:"product"`
	Resources []Resource `json:"resources"`
	Scopes    []string   `json:"scopes"`
}

// Authorizations represents the full authorization structure
type Authorizations struct {
	Policies []Policy `json:"policies"`
}

// AdminKeyResponse represents the transformed response format
type AdminKeyResponse struct {
	ID                 string         `json:"id"`
	Value              string         `json:"value"`
	Status             string         `json:"status"`
	OwnerType          string         `json:"owner_type"`
	OwnerID            string         `json:"owner_id"`
	IssuerServiceID    string         `json:"issuer_service_id"`
	AudienceServiceIDs []string       `json:"audience_service_ids"`
	Description        string         `json:"description,omitempty"`
	CreatedAt          string         `json:"created_at"`
	ExpiresAt          string         `json:"expires_at"`
	Authorizations     Authorizations `json:"authorizations"`
}

// FormatTime converts a Unix timestamp to ISO8601 format
func FormatTime(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).UTC().Format(time.RFC3339)
}
