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
package geo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/NVIDIA/nvcf-go/pkg/nvkit/auth"
	"github.com/NVIDIA/nvcf-go/pkg/nvkit/clients"
	"github.com/goccy/go-json"
	"go.uber.org/zap"
)

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type clientGetter interface {
	Client(context.Context) httpDoer
}

type glpsClient struct {
	client      clientGetter
	GeoGLPSFqdn string
}

type clientWrapper struct {
	client *clients.HTTPClient
}

func (c clientWrapper) Client(ctx context.Context) httpDoer {
	return c.client.Client(ctx)
}

type glpsResponse struct {
	GeoLocation ipGeoData `json:"geoLocation"`
}

type ipGeoData struct {
	CountryName string `json:"countryName"`
	RegionName  string `json:"subdivision1Name"`
	CityName    string `json:"cityName"`
	ISPName     string `json:"isp"`
}

func newGlpsClient(geoSsaAddr, geoGlpsAddr, secretsPath string) (*glpsClient, error) {
	authnConfig := &auth.AuthnConfig{
		OIDCConfig: &auth.ProviderConfig{
			Host:            geoSsaAddr,
			CredentialsFile: secretsPath,
			Scopes:          []string{"geolocation-resolve"},
		},
	}

	glpsClientConfig := clients.HTTPClientConfig{BaseClientConfig: &clients.BaseClientConfig{
		Addr:     geoGlpsAddr,
		AuthnCfg: authnConfig,
	}}

	httpClient, err := clients.DefaultHTTPClient(&glpsClientConfig, func(_ string, r *http.Request) string {
		return r.URL.Path
	})
	if err != nil {
		zap.L().Error("error constructing the HTTP client", zap.Error(err))
		return nil, err
	}

	return &glpsClient{GeoGLPSFqdn: geoGlpsAddr, client: clientWrapper{client: httpClient}}, nil
}

func (g *glpsClient) getGeoDataFromIp(ctx context.Context, ipAddress net.IP) (ipGeoData, error) {
	params := url.Values{
		"ipv4": {ipAddress.String()},
	}
	glpsUrl, err := url.Parse(g.GeoGLPSFqdn)
	if err != nil {
		zap.L().Error("failed to parse GLPS client URL", zap.Error(err))
		return ipGeoData{}, err
	}
	glpsUrl.Path = "/v1/geolocation/full"
	glpsUrl.RawQuery = params.Encode()
	fullUrl := glpsUrl.String()

	// Create the request
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullUrl, nil)
	if err != nil {
		zap.L().Error("error creating the request", zap.Error(err))
		return ipGeoData{}, err
	}

	response, err := g.client.Client(ctx).Do(req)
	if err != nil {
		zap.L().Warn("error doing the request", zap.String("url", fullUrl), zap.Error(err))
		return ipGeoData{}, err
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			zap.L().Error("error closing response body:", zap.Error(err))
		}
	}()

	// This is not automatically caught as part of the above error, since it is still a valid response
	if response.StatusCode != http.StatusOK {
		zap.L().Error("HTTP status for GLPS request was not OK: %w", zap.Int("statusCode", response.StatusCode), zap.Error(err))
		return ipGeoData{}, errors.New("non-200 status code")
	}
	if response.Header.Get("Content-Type") != "application/json" {
		zap.L().Error("the header content-type is not JSON: %w", zap.String("content type", response.Header.Get("Content-Type")), zap.Error(err))
		return ipGeoData{}, errors.New("the header content-type is not JSON")
	}

	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		return ipGeoData{}, fmt.Errorf("unexpected content type: %s", contentType)
	}

	geoData, err := parseJsonResponseToMap(response.Body)
	if err != nil {
		zap.L().Error("error parsing the response", zap.Error(err))
		return ipGeoData{}, err
	}

	return geoData, nil
}

func parseJsonResponseToMap(responseBody io.Reader) (ipGeoData, error) {
	var geoResponse glpsResponse
	err := json.NewDecoder(responseBody).Decode(&geoResponse)
	if err != nil {
		zap.L().Error("error parsing the response body", zap.Error(err))
		return ipGeoData{}, err
	}
	// TODO validate the quality of the glpsResponse
	return geoResponse.GeoLocation, nil
}
