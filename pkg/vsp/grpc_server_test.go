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

package vsp

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	pb "github.com/openshift/dpu-operator/dpu-api/gen"
	opi "github.com/opiproject/opi-api/v1/gen/go/lifecycle/v1alpha1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/kartikeyg0104/opi-nvidia-dpf-adapter/pkg/discovery"
)

func TestGetDevicesUsesEnumerator(t *testing.T) {
	ctx, conn, errc, cancel := startTestServer(t)

	got, err := pb.NewDeviceServiceClient(conn).GetDevices(ctx, &pb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	dev, ok := got.Devices["0000:03:00.0"]
	if !ok {
		t.Fatalf("devices=%v", got.Devices)
	}
	assertBlueField(t, dev.GetID(), dev.GetHealth())

	ip, err := pb.NewLifeCycleServiceClient(conn).Init(ctx, &pb.InitRequest{DpuMode: false})
	if err != nil {
		t.Fatal(err)
	}
	if ip.GetIp() != defaultIP || ip.GetPort() != defaultPort {
		t.Fatalf("%+v", ip)
	}

	waitStop(t, cancel, errc)
}

func TestOpiAPIGetDevicesUsesEnumerator(t *testing.T) {
	ctx, conn, errc, cancel := startTestServer(t)

	got, err := opi.NewDeviceServiceClient(conn).GetDevices(ctx, &emptypb.Empty{})
	if err != nil {
		t.Fatal(err)
	}
	dev, ok := got.Devices["0000:03:00.0"]
	if !ok {
		t.Fatalf("devices=%v", got.Devices)
	}
	assertBlueField(t, dev.GetID(), dev.GetHealth())

	ip, err := opi.NewLifeCycleServiceClient(conn).Init(ctx, &opi.InitRequest{DpuMode: true})
	if err != nil {
		t.Fatal(err)
	}
	if ip.GetIp() != defaultIP || ip.GetPort() != defaultPort {
		t.Fatalf("%+v", ip)
	}

	waitStop(t, cancel, errc)
}

func TestNetworkFunctionAcknowledgesWithoutProvisioning(t *testing.T) {
	ctx, conn, errc, cancel := startTestServer(t)
	nf := pb.NewNetworkFunctionServiceClient(conn)
	req := &pb.NFRequest{Input: "00:00:00:00:00:01", Output: "00:00:00:00:00:02"}

	if _, err := nf.CreateNetworkFunction(ctx, req); err != nil {
		t.Fatalf("CreateNetworkFunction: %v", err)
	}
	if _, err := nf.DeleteNetworkFunction(ctx, req); err != nil {
		t.Fatalf("DeleteNetworkFunction: %v", err)
	}

	waitStop(t, cancel, errc)
}

func startTestServer(t *testing.T) (context.Context, *grpc.ClientConn, chan error, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	sock := fmt.Sprintf("%s/nvsp-%d.sock", os.TempDir(), time.Now().UnixNano())
	t.Cleanup(func() { _ = os.Remove(sock) })
	enum := discovery.MockEnumerator{
		Devices: []discovery.Device{
			discovery.StaticDevice("MT25066004A1", "0000:03:00.0", "BlueField-3"),
		},
	}
	srv := NewServer(enum, sock)
	errc := make(chan error, 1)
	go func() { errc <- srv.Start(ctx) }()

	conn := waitDial(t, sock)
	t.Cleanup(func() { _ = conn.Close() })
	return ctx, conn, errc, cancel
}

func assertBlueField(t *testing.T, id, health string) {
	t.Helper()
	if id != "MT25066004A1" || health != healthOK {
		t.Fatalf("id=%s health=%s", id, health)
	}
}

func waitStop(t *testing.T, cancel context.CancelFunc, errc chan error) {
	t.Helper()
	cancel()
	select {
	case <-errc:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop")
	}
}

func waitDial(t *testing.T, sock string) *grpc.ClientConn {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	conn, err := grpc.NewClient("passthrough:///"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		}),
	)
	if err != nil {
		t.Fatalf("dial %s: %v", sock, err)
	}
	return conn
}
