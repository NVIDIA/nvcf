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
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/goccy/go-json"
	"go.opentelemetry.io/contrib/instrumentation/github.com/aws/aws-sdk-go-v2/otelaws"
	"go.uber.org/zap"
)

type geoDB struct {
	// immutable
	s3Client                 *s3.Client
	s3RoutingTableBucketName string
	s3RoutingTableObjectKey  string
	routingTableValidityDays int

	// mutable
	routingTable atomic.Pointer[routingTable]
	// locks UPDATES of the routing table. multiple updates should not happen concurrently.
	routingTableUpdateLock       sync.Mutex
	routingTableLastModifiedTime time.Time
}

type MetroInfo struct {
	IdealMetroAreas []string `json:"idealMetroAreas"`
}

type Cities struct {
	Default MetroInfo            `json:"default"`
	ISPs    map[string]MetroInfo `json:"ISPs"`
}

type Regions struct {
	Default MetroInfo         `json:"default"`
	Cities  map[string]Cities `json:"Cities"`
}

type Countries struct {
	Default MetroInfo          `json:"default"`
	Regions map[string]Regions `json:"Regions"`
}

type routingTable struct {
	Default   MetroInfo            `json:"default"`
	Countries map[string]Countries `json:"Countries"`
}

func newGeoDB(ctx context.Context, geoTableS3Region, geoTableS3BucketName string, routingTableValidityDays int) *geoDB {
	geoDB := &geoDB{
		s3Client:                 newS3Client(ctx, geoTableS3Region),
		s3RoutingTableBucketName: geoTableS3BucketName,
		s3RoutingTableObjectKey:  "geo_routing_table.json",
		routingTableValidityDays: routingTableValidityDays,
	}
	geoDB.backgroundUpdateRoutingMapFromS3(ctx)
	return geoDB
}

func newS3Client(ctx context.Context, geoTableS3Region string) *s3.Client {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(geoTableS3Region))
	if err != nil {
		zap.L().Error("failed to create S3 session", zap.Error(err))
		return nil
	}
	otelaws.AppendMiddlewares(&cfg.APIOptions)
	return s3.NewFromConfig(cfg)
}

func (g *geoDB) isGeoDataFresh() bool {
	// The data is fresh if it's been updated in the last week
	if g.routingTableLastModifiedTime.IsZero() {
		return false
	}
	return g.routingTableLastModifiedTime.After(time.Now().AddDate(0, 0, -g.routingTableValidityDays))
}

func (g *geoDB) backgroundUpdateRoutingMapFromS3(ctx context.Context) {
	zap.L().Debug("background update started")
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	go func() {
		defer cancel()
		err := g.updateRoutingMapFromS3(ctx)
		zap.L().Debug("background update ended", zap.Error(err))
	}()
}

func (g *geoDB) updateRoutingMapFromS3(ctx context.Context) error {
	g.routingTableUpdateLock.Lock()
	defer g.routingTableUpdateLock.Unlock()

	isGeoDataFresh := g.isGeoDataFresh()
	if isGeoDataFresh {
		zap.L().Debug("geo data was fresh")
		return nil
	}

	if g.s3Client == nil {
		err := fmt.Errorf("s3 client is nil")
		zap.L().Warn("s3 client is nil", zap.Error(err))
		return err
	}

	result, err := g.s3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(g.s3RoutingTableBucketName),
		Key:    aws.String(g.s3RoutingTableObjectKey),
	})
	if err != nil {
		zap.L().Error("failed to update routing map from S3", zap.Error(err))
		return err
	}
	defer result.Body.Close()

	decoder := json.NewDecoder(result.Body)

	var candidateRoutingTable routingTable
	if err = decoder.Decode(&candidateRoutingTable); err != nil {
		zap.L().Error("error decoding routingTable from S3", zap.Error(err))
		return err
	}
	if geoTableIsValid(&candidateRoutingTable) {
		g.routingTable.Store(&candidateRoutingTable)
		g.routingTableLastModifiedTime = time.Now()
	} else {
		err = fmt.Errorf("invalid routing table from S3")
		zap.L().Warn("invalid routing table from S3", zap.Error(err))
		return err
	}
	zap.L().Info("successfully updated routing map from S3")
	return nil
}

func getMetroAreasFromGeoTable(routingTable *routingTable, geo ipGeoData) []string {
	country := routingTable.Countries[geo.CountryName]
	if len(country.Regions) == 0 {
		return routingTable.Default.IdealMetroAreas
	}

	region := country.Regions[geo.RegionName]
	if len(region.Cities) == 0 {
		return country.Default.IdealMetroAreas
	}

	city := region.Cities[geo.CityName]
	if len(city.ISPs) == 0 {
		return region.Default.IdealMetroAreas
	}

	isp := city.ISPs[geo.ISPName]
	if len(isp.IdealMetroAreas) == 0 {
		return city.Default.IdealMetroAreas
	}

	return isp.IdealMetroAreas
}

func (g *geoDB) updateGeoTableIfNil(ctx context.Context) error {
	// Check if routingTable is nil; if it is, update it and check again
	if g.routingTable.Load() == nil {
		// Given the routingTable was nil, update it synchronously
		err := g.updateRoutingMapFromS3(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *geoDB) getIdealMetroAreas(ctx context.Context, geo ipGeoData) ([]string, error) {
	err := g.updateGeoTableIfNil(ctx)
	if err != nil {
		return nil, err
	}
	table := g.routingTable.Load()
	g.backgroundUpdateRoutingMapFromS3(ctx)
	return getMetroAreasFromGeoTable(table, geo), nil
}

func geoTableIsValid(routingTable *routingTable) bool {
	for _, country := range routingTable.Countries {
		if len(country.Regions) > 0 {
			return true
		}
	}
	return false
}
