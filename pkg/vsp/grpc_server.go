/*
Copyright 2026 Kartikey Gupta.

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

// Package vsp is the NVIDIA Vendor-Specific Plugin gRPC surface.
//
// The in-tree dpu-operator daemon dials a unix socket and expects
// LifeCycle.Init plus DeviceService.GetDevices. One listener multiplexes:
//
//   - opi-api lifecycle (opi_api.lifecycle.v1alpha1.*) — what GrpcPlugin
//     currently dials for Init/GetDevices
//   - dpu-api Vendor.* — NetworkFunction plus the same handshake for the
//     vendor-specific path
//
// Generated stubs are imported; this repo does not run protoc.
package vsp

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	pb "github.com/openshift/dpu-operator/dpu-api/gen"
	opi "github.com/opiproject/opi-api/v1/gen/go/lifecycle/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/discovery"
)

// DefaultSocket is where dpu-operator's GrpcPlugin dials the VSP.
const DefaultSocket = "/var/run/dpu-daemon/vendor-plugin/vendor-plugin.sock"

const (
	defaultIP   = "127.0.0.1"
	defaultPort = int32(50051)
	healthOK    = "Healthy"
)

// Server implements the vendor plugin RPCs the upstream daemon dials.
type Server struct {
	pb.UnimplementedLifeCycleServiceServer
	pb.UnimplementedDeviceServiceServer
	pb.UnimplementedNetworkFunctionServiceServer
	pb.UnimplementedHeartbeatServiceServer

	Enumerator discovery.Enumerator
	SocketPath string
	Log        logr.Logger

	grpcServer *grpc.Server
}

// NewServer returns a VSP gRPC server. SocketPath defaults to DefaultSocket.
func NewServer(enum discovery.Enumerator, socket string) *Server {
	if socket == "" {
		socket = DefaultSocket
	}
	return &Server{
		Enumerator: enum,
		SocketPath: socket,
		Log:        ctrl.Log.WithName("nvidia-vsp"),
	}
}

// Init answers the daemon's health handshake.
func (s *Server) Init(_ context.Context, req *pb.InitRequest) (*pb.IpPort, error) {
	s.Log.Info("Init", "dpuMode", req.GetDpuMode(), "dpuIdentifier", req.GetDpuIdentifier())
	return &pb.IpPort{Ip: defaultIP, Port: defaultPort}, nil
}

// GetDevices reports BlueField functions found by the enumerator.
func (s *Server) GetDevices(_ context.Context, _ *pb.Empty) (*pb.DeviceListResponse, error) {
	if s.Enumerator == nil {
		return nil, fmt.Errorf("no hardware enumerator configured")
	}
	found, err := s.Enumerator.Enumerate()
	if err != nil {
		return nil, fmt.Errorf("enumerate nvidia dpu: %w", err)
	}
	devices := make(map[string]*pb.Device, len(found))
	for _, d := range found {
		id := d.PCIAddress
		devices[id] = &pb.Device{
			ID:     d.SerialNumber,
			Health: healthOK,
		}
	}
	s.Log.Info("GetDevices", "count", len(devices))
	return &pb.DeviceListResponse{Devices: devices}, nil
}

// SetNumVfs is a no-op echo until SR-IOV is required.
func (s *Server) SetNumVfs(_ context.Context, req *pb.VfCount) (*pb.VfCount, error) {
	s.Log.Info("SetNumVfs", "vfCnt", req.GetVfCnt())
	return &pb.VfCount{VfCnt: req.GetVfCnt()}, nil
}

// CreateNetworkFunction is a stub; NVIDIA network functions are Helm charts
// translated out-of-tree, not created over this RPC.
func (s *Server) CreateNetworkFunction(_ context.Context, req *pb.NFRequest) (*pb.Empty, error) {
	s.Log.Info("CreateNetworkFunction", "input", req.GetInput(), "output", req.GetOutput())
	return &pb.Empty{}, nil
}

// DeleteNetworkFunction is a stub matching CreateNetworkFunction.
func (s *Server) DeleteNetworkFunction(_ context.Context, req *pb.NFRequest) (*pb.Empty, error) {
	s.Log.Info("DeleteNetworkFunction", "input", req.GetInput(), "output", req.GetOutput())
	return &pb.Empty{}, nil
}

// Ping reports the VSP process as healthy.
func (s *Server) Ping(_ context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{
		Timestamp:   time.Now().Unix(),
		ResponderId: "opi-nvidia-vsp",
		Healthy:     true,
	}, nil
}

// Start serves gRPC on the unix socket until ctx is cancelled.
// It implements manager.Runnable so cmd/vsp can mgr.Add it.
func (s *Server) Start(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create vendor plugin socket dir: %w", err)
	}
	if err := os.Remove(s.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale vendor plugin socket: %w", err)
	}
	ln, err := net.Listen("unix", s.SocketPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.SocketPath, err)
	}

	s.grpcServer = grpc.NewServer()
	pb.RegisterLifeCycleServiceServer(s.grpcServer, s)
	pb.RegisterDeviceServiceServer(s.grpcServer, s)
	pb.RegisterNetworkFunctionServiceServer(s.grpcServer, s)
	pb.RegisterHeartbeatServiceServer(s.grpcServer, s)
	hs := &opiHandshake{s: s}
	opi.RegisterLifeCycleServiceServer(s.grpcServer, hs)
	opi.RegisterDeviceServiceServer(s.grpcServer, hs)
	reflection.Register(s.grpcServer)

	s.Log.Info("serving vendor plugin", "socket", s.SocketPath)

	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
		_ = ln.Close()
	}()

	if err := s.grpcServer.Serve(ln); err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// NeedLeaderElection reports that the unix socket must run on every node,
// not only the elected leader.
func (s *Server) NeedLeaderElection() bool {
	return false
}
