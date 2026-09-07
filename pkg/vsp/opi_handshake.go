/*
Copyright 2026 Kartikey Gupta.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

    10|Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package vsp

import (
	"context"

	pb "github.com/openshift/dpu-operator/dpu-api/gen"
	opi "github.com/opiproject/opi-api/v1/gen/go/lifecycle/v1alpha1"
	"google.golang.org/protobuf/types/known/emptypb"
)

// opiHandshake adapts the same enumerator-backed methods onto the opi-api
// LifeCycle/Device services. Those types cannot be embedded on Server: Init
// and GetDevices collide with the dpu-api embeddings.
type opiHandshake struct {
	opi.UnimplementedLifeCycleServiceServer
	opi.UnimplementedDeviceServiceServer
	s *Server
}

// Init answers the daemon GrpcPlugin handshake.
func (o *opiHandshake) Init(ctx context.Context, req *opi.InitRequest) (*opi.IpPort, error) {
	ip, err := o.s.Init(ctx, &pb.InitRequest{
		DpuMode:       req.GetDpuMode(),
		DpuIdentifier: req.GetDpuIdentifier(),
	})
	if err != nil {
		return nil, err
	}
	return &opi.IpPort{Ip: ip.GetIp(), Port: ip.GetPort()}, nil
}

// GetDevices reuses PCIEnumerator via the dpu-api implementation.
func (o *opiHandshake) GetDevices(ctx context.Context, _ *emptypb.Empty) (*opi.DeviceListResponse, error) {
	got, err := o.s.GetDevices(ctx, &pb.Empty{})
	if err != nil {
		return nil, err
	}
	devices := make(map[string]*opi.Device, len(got.GetDevices()))
	for k, d := range got.GetDevices() {
		devices[k] = &opi.Device{ID: d.GetID(), Health: d.GetHealth()}
	}
	return &opi.DeviceListResponse{Devices: devices}, nil
}

// SetNumVfs is a no-op echo until SR-IOV is required.
func (o *opiHandshake) SetNumVfs(ctx context.Context, req *opi.VfCount) (*opi.VfCount, error) {
	got, err := o.s.SetNumVfs(ctx, &pb.VfCount{VfCnt: req.GetVfCnt()})
	if err != nil {
		return nil, err
	}
	return &opi.VfCount{VfCnt: got.GetVfCnt()}, nil
}
