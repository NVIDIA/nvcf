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
	"net"

	"go.uber.org/zap"
)

type geoGetter interface {
	getGeoDataFromIp(context.Context, net.IP) (ipGeoData, error)
}

type metroAreaGetter interface {
	getIdealMetroAreas(context.Context, ipGeoData) ([]string, error)
}

type IPLookupService struct {
	geoDB metroAreaGetter
	glps  geoGetter
}

func NewGeoIPLookupService(ctx context.Context, geoSsaAddr, geoGlpsAddr, geoTableS3Region, geoTableS3BucketName, secretsPath string, routingTableValidityDays int) (*IPLookupService, error) {
	glps, err := newGlpsClient(geoSsaAddr, geoGlpsAddr, secretsPath)
	if err != nil {
		return nil, err
	}
	geoLookup := &IPLookupService{
		glps:  glps,
		geoDB: newGeoDB(ctx, geoTableS3Region, geoTableS3BucketName, routingTableValidityDays),
	}
	return geoLookup, nil
}

func (g *IPLookupService) LookupRegions(ctx context.Context, ipAddress net.IP) []string {
	if ipAddress == nil || ipAddress.IsUnspecified() {
		zap.L().Error("invalid or nil IP address provided", zap.Stringer("ip", ipAddress))
		return nil
	}

	geoData, err := g.glps.getGeoDataFromIp(ctx, ipAddress)
	if err != nil {
		zap.L().Error("failed to get the ipGeoData from IP address", zap.String("ip", ipAddress.String()), zap.Error(err))
		return nil
	}

	idealZones, err := g.geoDB.getIdealMetroAreas(ctx, geoData)
	if err != nil {
		zap.L().Error("failed to get the IdealZones from ipGeoData", zap.Error(err))
		return nil
	}
	return idealZones
}

type DisabledGeoIPLookupService struct{}

func (d DisabledGeoIPLookupService) LookupRegions(ctx context.Context, clientIP net.IP) []string {
	return nil
}
