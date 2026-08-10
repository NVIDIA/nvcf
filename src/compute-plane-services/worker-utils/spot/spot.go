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

package main

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"github.com/goccy/go-json"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"time"
)

//go:embed clusterGroups.json
var clusterGroups []byte

var instanceRequests = make(map[uuid.UUID]*instanceRequest)
var instanceRequestsLock = sync.RWMutex{}

func main() {
	http.HandleFunc(`/`, func(writer http.ResponseWriter, request *http.Request) {
		log.Printf("responding to %s %s", request.Method, request.RequestURI)
		writer.Header().Set("Content-Type", "application/json")
		uri, err := url.Parse(request.RequestURI)
		if err != nil {
			writer.WriteHeader(500)
			return
		}
		match, _ := regexp.MatchString("/v1/si/accounts/.+/clusterGroups", uri.Path)
		if match {
			_, _ = writer.Write(clusterGroups)
			return
		}

		var response any
		switch uri.Query().Get("Action") {
		case "RequestSpotInstances":
			instanceCount, _ := strconv.Atoi(request.PostFormValue("InstanceCount"))
			instanceType := request.PostFormValue("LaunchSpecification.InstanceType")
			containerImage := request.PostFormValue("LaunchSpecification.ContainerImage")
			environment := request.PostFormValue("LaunchSpecification.Environment")
			response = requestSpotInstances(instanceCount, instanceType, containerImage, environment)
		case "DescribeSpotInstanceRequests":
			instanceRequestIds := uri.Query()["SpotInstanceRequestId"]
			response = describeSpotInstanceRequests(lo.Map(instanceRequestIds, func(item string, index int) uuid.UUID {
				return uuid.MustParse(item)
			})...)
		case "TerminateInstances":
			instanceIds := uri.Query()["InstanceId"]
			response = terminateInstances(instanceIds)
		case "TerminateSpotInstanceRequest":
			instanceRequestId := uuid.MustParse(uri.Query().Get("RequestId"))
			response = terminateSpotInstanceRequests(instanceRequestId)
		default:
			writer.WriteHeader(404)
			return
		}
		err = json.NewEncoder(writer).Encode(response)
		if err != nil {
			writer.WriteHeader(500)
			return
		}
	})
	log.Println("listening on :9123")
	log.Fatalln(http.ListenAndServe(":9123", nil))
}

func terminateSpotInstanceRequests(instanceRequestId uuid.UUID) any {
	instanceRequestsLock.Lock()
	defer instanceRequestsLock.Unlock()
	delete(instanceRequests, instanceRequestId)
	return map[string]any{}
}

func terminateInstances(instanceIds []string) any {
	instanceIdSet := make(map[uuid.UUID]struct{})
	for _, instanceId := range instanceIds {
		instanceIdSet[uuid.MustParse(instanceId)] = struct{}{}
	}
	instanceRequestsLock.Lock()
	defer instanceRequestsLock.Unlock()
	for _, instances := range instanceRequests {
		for _, instance := range instances.instances {
			if _, ok := instanceIdSet[instance.id]; ok {
				instance.status = "CANCELED"
			}
		}
	}
	return map[string]any{}
}

func describeSpotInstanceRequests(instanceRequestIDs ...uuid.UUID) any {
	var responses []InstanceRequest
	instanceRequestsLock.RLock()
	defer instanceRequestsLock.RUnlock()
	for _, instanceRequestID := range instanceRequestIDs {
		request, ok := instanceRequests[instanceRequestID]
		if !ok {
			continue
		}
		for _, instance := range request.instances {
			response := InstanceRequest{
				CreateTime: instance.createTime,
				InstanceId: lo.ToPtr(instance.id.String()),
				LaunchSpecification: LaunchSpecification{
					InstanceType:   instance.instanceType,
					ContainerImage: instance.containerImage,
					Placement:      &Placement{AvailabilityZone: "my laptop"},
				},
				LaunchedAvailabilityZone: lo.ToPtr("local"),
				SpotInstanceRequestId:    instanceRequestID.String(),
				SpotCloudProvider:        lo.ToPtr("GFN"),
				State:                    "active",
				Status: Status{
					Code:       "running",
					Message:    "running",
					UpdateTime: instance.createTime,
				},
				InstanceInterruptionBehavior: "terminate",
				InstanceState: &InstanceState{
					Code: 0,
					Name: "running",
				},
			}
			responses = append(responses, response)
		}
	}
	return DescribeSpotInstanceRequests{SpotInstanceRequests: responses}
}

type DescribeSpotInstanceRequests struct {
	SpotInstanceRequests []InstanceRequest `json:"SpotInstanceRequests"`
}

type InstanceRequest struct {
	CreateTime                   time.Time           `json:"CreateTime"`
	InstanceId                   *string             `json:"InstanceId"`
	LaunchSpecification          LaunchSpecification `json:"LaunchSpecification"`
	LaunchedAvailabilityZone     *string             `json:"LaunchedAvailabilityZone"`
	SpotInstanceRequestId        string              `json:"SpotInstanceRequestId"`
	SpotCloudProvider            *string             `json:"SpotCloudProvider"`
	State                        string              `json:"State"`
	Status                       Status              `json:"Status"`
	InstanceInterruptionBehavior string              `json:"InstanceInterruptionBehavior"`
	InstanceState                *InstanceState      `json:"InstanceState"`
}

type Placement struct {
	AvailabilityZone string `json:"AvailabilityZone"`
}

type LaunchSpecification struct {
	InstanceType   string     `json:"InstanceType"`
	ContainerImage string     `json:"ContainerImage"`
	Placement      *Placement `json:"Placement"`
}

type Status struct {
	Code       string    `json:"Code"`
	Message    string    `json:"Message"`
	UpdateTime time.Time `json:"UpdateTime"`
}

type InstanceState struct {
	Code int    `json:"Code"`
	Name string `json:"Name"`
}

type instanceRequest struct {
	id          uuid.UUID
	instances   []*instance
	requestTime time.Time
}
type instance struct {
	id             uuid.UUID
	status         string
	containerImage string
	instanceType   string
	environment    string
	createTime     time.Time
}

func requestSpotInstances(instanceCount int, instanceType, containerImage, environment string) map[string]uuid.UUID {
	requestId := uuid.New()
	now := time.Now()
	var instances []*instance
	for i := 0; i < instanceCount; i++ {
		instances = append(instances, &instance{
			id:             uuid.New(),
			status:         "ACTIVE",
			containerImage: containerImage,
			instanceType:   instanceType,
			environment:    environment,
			createTime:     now,
		})
	}
	for _, instance := range instances {
		decodeString, _ := base64.StdEncoding.DecodeString(instance.environment)
		log.Printf("instance created id: %s, writing env to file", instance.id)
		file, _ := os.Create(".env")
		_, _ = file.Write([]byte(fmt.Sprintf("INSTANCE_ID=%s\n", instance.id.String())))
		_, _ = file.Write(decodeString)
		_ = file.Close()
	}
	instanceRequestsLock.Lock()
	defer instanceRequestsLock.Unlock()
	instanceRequests[requestId] = &instanceRequest{
		id:          requestId,
		instances:   instances,
		requestTime: now,
	}
	return map[string]uuid.UUID{
		"requestId": requestId,
	}
}
