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
package invocation

import (
	"context"
	"errors"
	"nvcf-grpc-proxy/nvcf/pb"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
)

type StatefulRequestRegistration struct {
	lookupStream        jetstream.Stream
	streamName          string
	streamSubjectPrefix string
}

func NewStatefulRequestRegistration(ctx context.Context, js jetstream.JetStream, jetstreamPlacementTag, region string) (*StatefulRequestRegistration, error) {
	streamName := "stateful_session_lookup_" + region
	streamSubjectPrefix := "stateful_session.lookup." + region
	lookupStream, err := js.Stream(ctx, streamName)
	if errors.Is(err, jetstream.ErrStreamNotFound) {
		lookupStream, err = js.CreateStream(ctx, jetstream.StreamConfig{
			Name:              streamName,
			Subjects:          []string{streamSubjectPrefix + ".>"},
			Retention:         jetstream.LimitsPolicy,
			MaxMsgs:           100_000,
			Discard:           jetstream.DiscardNew,
			MaxAge:            1 * time.Hour,
			MaxMsgsPerSubject: 1,
			Storage:           jetstream.MemoryStorage,
			Replicas:          3,
			Placement: &jetstream.Placement{
				Tags: []string{jetstreamPlacementTag},
			},
			AllowDirect: true,
		})
		if err != nil {
			// another instance must have created the stream already
			if errors.Is(err, jetstream.ErrStreamNameAlreadyInUse) {
				lookupStream, err = js.Stream(ctx, streamName)
				if err != nil {
					return nil, err
				}
			} else {
				return nil, err
			}
		}
	}

	return &StatefulRequestRegistration{lookupStream, streamName, streamSubjectPrefix}, nil
}

func (s *StatefulRequestRegistration) LookupStatefulSession(ctx context.Context, requestId uuid.UUID) (*pb.StatefulSessionTracking, error) {
	subject := s.subjectForLookups(requestId)
	msg, err := s.lookupStream.GetLastMsgForSubject(ctx, subject)
	if err != nil {
		return nil, err // may be jetstream.ErrMsgNotFound
	}
	statefulSessionTracking := pb.StatefulSessionTracking{}
	err = proto.Unmarshal(msg.Data, &statefulSessionTracking)
	if err != nil {
		return nil, err
	}
	return &statefulSessionTracking, nil
}

func (s *StatefulRequestRegistration) subjectForLookups(requestId uuid.UUID) string {
	return s.streamSubjectPrefix + ".*." + requestId.String()
}
