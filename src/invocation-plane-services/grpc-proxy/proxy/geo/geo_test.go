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
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/docker/go-connections/nat"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/testcontainers/testcontainers-go"
	localstack "github.com/testcontainers/testcontainers-go/modules/localstack"
	"go.uber.org/zap"
)

var _ metroAreaGetter = (*mockGeoDB)(nil)

type mockGeoDB struct {
	mock.Mock
}

type mockGlpsClient struct {
	mock.Mock
	httpClient clientGetter
}

func (g *mockGlpsClient) getGeoDataFromIp(ctx context.Context, ipAddress net.IP) (ipGeoData, error) {
	args := g.Called(ctx, ipAddress)
	return args.Get(0).(ipGeoData), args.Error(1)
}

func (g *mockGeoDB) getIdealMetroAreas(ctx context.Context, geoData ipGeoData) ([]string, error) {
	args := g.Called(ctx, geoData)
	val := args.Get(0)
	if val == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).([]string), args.Error(1)
}

func TestIPLookupService_LookupRegions(t *testing.T) {
	// Define test cases
	tests := []struct {
		name           string
		ipAddress      net.IP
		geoData        ipGeoData
		geoDataErr     error
		idealZones     []string
		idealZonesErr  error
		expectedResult []string
	}{
		{
			name:      "Successful lookup",
			ipAddress: net.ParseIP("1.2.3.4"),
			geoData: ipGeoData{
				CountryName: "TestCountry",
				RegionName:  "TestRegion",
				CityName:    "TestCity",
				ISPName:     "TestISP",
			},
			idealZones:     []string{"Zone1", "Zone2"},
			expectedResult: []string{"Zone1", "Zone2"},
		},
		{
			name:           "GeoData fetch error",
			ipAddress:      net.ParseIP("1.2.3.4"),
			geoDataErr:     errors.New("geo data error"),
			expectedResult: nil,
		},
		{
			name:      "IdealZones fetch error",
			ipAddress: net.ParseIP("1.2.3.4"),
			geoData: ipGeoData{
				CountryName: "TestCountry",
				RegionName:  "TestRegion",
				CityName:    "TestCity",
				ISPName:     "TestISP",
			},
			idealZonesErr:  errors.New("ideal zones error"),
			expectedResult: nil,
		},
		{
			name:           "Invalid IP address",
			ipAddress:      nil, // Invalid IP
			expectedResult: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			// Create mock geoGetter
			mockGeo := new(mockGlpsClient)
			mockMetro := new(mockGeoDB)
			// Only set up expectations if the IP address is valid
			if tt.ipAddress != nil && !tt.ipAddress.IsUnspecified() {
				// Set up expectation for getGeoDataFromIp
				mockGeo.On("getGeoDataFromIp", ctx, tt.ipAddress).Return(tt.geoData, tt.geoDataErr).Once()

				if tt.geoDataErr == nil {
					// Set up mock for getIdealMetroAreas only if getGeoDataFromIp succeeds
					mockMetro.On("getIdealMetroAreas", ctx, tt.geoData).Return(tt.idealZones, tt.idealZonesErr).Once()
				} else {
					// No need to set up mockMetro, as it won't be called
				}
			} else {
				// IP address is invalid; methods won't be called
			}

			// Create IPLookupService with mocks
			ipLookupService := &IPLookupService{
				glps:  mockGeo,
				geoDB: mockMetro,
			}

			// Call the method under test
			result := ipLookupService.LookupRegions(ctx, tt.ipAddress)

			// Verify results
			assert.Equal(t, tt.expectedResult, result)

			// Assert that all expected methods were called
			mockGeo.AssertExpectations(t)
			mockMetro.AssertExpectations(t)
		})
	}
}
func getSampleRoutingJsonValid() []byte {
	return []byte(`
	{
	  "Countries": {
		"Turkey": {
		  "Regions": {
			"Mugla": {
			  "Cities": {
				"Mugla": {
				  "ISPs": {
					"Vodafone Turkey": {"idealMetroAreas": ["TKC-ESB", "TKC-IST", "SOF", "FRK", "LON"]},
					"KoycegizNet": {"idealMetroAreas": ["TKC-IST", "TKC-ESB", "SOF", "FRK", "LON"]},
					"Turk Telekom": {"idealMetroAreas": ["TKC-ESB", "TKC-IST", "SOF", "FRK", "LON"]},
					"Turkcell Superonline": {"idealMetroAreas": ["TKC-ESB", "TKC-IST", "SOF", "FRK", "LON"]},
					"TurkNet": {"idealMetroAreas": ["TKC-IST", "TKC-ESB", "SOF", "FRK", "LON"]},
					"National Academic Network and Information Center": {"idealMetroAreas": ["TKC-ESB", "TKC-IST", "LON", "FRK", "SOF"]}
				  },
				  "default": {"idealMetroAreas": ["TKC-IST", "TKC-ESB", "SOF", "FRK", "LON"]}
				}
			  },
			  "default": {"idealMetroAreas": ["TKC-IST", "TKC-ESB", "SOF", "FRK", "TWM-TPE"]}
			}
		  },
		  "default": {"idealMetroAreas": ["TKC-IST", "TKC-ESB", "SOF", "FRK", "TWM-TPE"]}
		}
	  },
	  "default": {"idealMetroAreas": ["TKC-IST", "TKC-ESB", "SOF", "FRK", "TWM-TPE"]}
	}
`)
}

func getTestConfig() *TestConfig {
	return &TestConfig{
		SecretsPath:              "vault/secrets.json",
		GeoSsaAddr:               "https://ikp3ttn3ycpy1gpxrubl0kut4l2dgvos1e3sxzsezt8.stg.ssa.nvidia.com",
		GeoGLPSAddr:              "https://glps-stg.nvidia.com",
		GeoTableS3Region:         "us-west-2",
		GeoTableS3BucketName:     "nv-nvcf-location-to-zone-route-maps-stg",
		RoutingTableValidityDays: 1,
	}
}

func setupS3Client(ctx context.Context, ls *localstack.LocalStackContainer) (*s3.Client, error) {
	mappedPort, err := ls.MappedPort(ctx, nat.Port("4566/tcp"))
	if err != nil {
		return nil, err
	}

	provider, err := testcontainers.NewDockerProvider()
	if err != nil {
		return nil, err
	}
	defer provider.Close()

	host, err := provider.DaemonHost(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := fmt.Sprintf("http://%s:%d", host, mappedPort.Int())

	awsCfg, err := config.LoadDefaultConfig(ctx,
		config.WithRegion("us-east-1"),
		config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider("test", "test", ""),
		),
	)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = &endpoint
	})
	return client, nil
}

func uploadByteSliceToS3(ctx context.Context, s3Client *s3.Client, bucket string, object []byte) error {
	// Upload the file to S3
	_, err := s3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String("geo_routing_table.json"),
		Body:   bytes.NewReader(object),
	})
	if err != nil {
		return fmt.Errorf("failed to upload file to S3: %v", err)
	}

	return nil
}

func createLocalstackWithBucket(ctx context.Context, bucket string) (*s3.Client, error) {
	ls, err := localstack.Run(ctx, "localstack/localstack:3.8.0")
	if err != nil {
		log.Fatalf("Failed to run localstack: %v", err)
		return nil, err
	}

	s3Client, err := setupS3Client(ctx, ls)
	_, err = s3Client.CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
	})
	if err != nil {
		log.Fatalf("Failed to create bucket: %v", err)
		return nil, err
	}
	return s3Client, nil
}

func TestMain(m *testing.M) {
	logger, _ := zap.NewProduction()
	defer logger.Sync()

	// Set the global logger
	zap.ReplaceGlobals(logger)

	m.Run()
}

type TestConfig struct {
	SecretsPath              string
	GeoSsaAddr               string
	GeoGLPSAddr              string
	GeoTableS3Region         string
	GeoTableS3BucketName     string
	RoutingTableValidityDays int
}

func TestGeoDB_IndexRoutingTable(t *testing.T) {
	tests := []struct {
		name     string
		geoData  ipGeoData
		expected []string
	}{
		{
			name: "Nonexistent Countries, Regions, Cities, and ISPs",
			geoData: ipGeoData{
				CountryName: "NonexistentCountry",
				RegionName:  "NonexistentRegion",
				CityName:    "NonexistentCity",
				ISPName:     "NonexistentISP",
			},
			expected: []string{"TKC-IST", "TKC-ESB", "SOF", "FRK", "TWM-TPE"},
		},
		{
			name: "Existing Countries, non-existent Regions, Cities, and ISPs",
			geoData: ipGeoData{
				CountryName: "Turkey",
				RegionName:  "NonexistentRegion",
				CityName:    "NonexistentCity",
				ISPName:     "NonexistentISP",
			},
			expected: []string{"TKC-IST", "TKC-ESB", "SOF", "FRK", "TWM-TPE"},
		},
		{
			name: "Existing Countries and Regions, non-existent Cities and ISPs",
			geoData: ipGeoData{
				CountryName: "Turkey",
				RegionName:  "Mugla",
				CityName:    "NonexistentCity",
				ISPName:     "NonexistentISP",
			},
			expected: []string{"TKC-IST", "TKC-ESB", "SOF", "FRK", "TWM-TPE"},
		},
		{
			name: "Existing Countries, Regions, and Cities, non-existent ISPs",
			geoData: ipGeoData{
				CountryName: "Turkey",
				RegionName:  "Mugla",
				CityName:    "Mugla",
				ISPName:     "NonexistentISP",
			},
			expected: []string{"TKC-IST", "TKC-ESB", "SOF", "FRK", "LON"},
		},
		{
			name: "Existing Countries, Regions, Cities, and ISPs",
			geoData: ipGeoData{
				CountryName: "Turkey",
				RegionName:  "Mugla",
				CityName:    "Mugla",
				ISPName:     "Turkcell Superonline",
			},
			expected: []string{"TKC-ESB", "TKC-IST", "SOF", "FRK", "LON"},
		},
	}
	configs := getTestConfig()

	ctx := context.Background()

	s3Client, err := createLocalstackWithBucket(ctx, configs.GeoTableS3BucketName)
	if err != nil {
		t.Fatal(err)
	}

	if err := uploadByteSliceToS3(ctx, s3Client, configs.GeoTableS3BucketName, getSampleRoutingJsonValid()); err != nil {
		t.Fatalf("Failed to upload file: %v", err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			geoDB := &geoDB{
				s3Client:                 s3Client,
				s3RoutingTableBucketName: configs.GeoTableS3BucketName,
				s3RoutingTableObjectKey:  "geo_routing_table.json",
				routingTableValidityDays: configs.RoutingTableValidityDays,
			}

			idealZones, err := geoDB.getIdealMetroAreas(ctx, test.geoData)
			if err != nil {
				t.Fatalf("getIdealMetroAreas err: %v", err)
			}
			if !slices.Equal(idealZones, test.expected) {
				t.Fatalf("idealZones != %v", test.expected)
			}
		})
	}
}
