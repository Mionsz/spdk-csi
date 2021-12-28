/*
Copyright (c) Arm Limited and Contributors.

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

package spdk

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"
	"k8s.io/utils/exec"
	"k8s.io/utils/mount"

	"github.com/spdk/spdk-csi/_out/spdk.io/sma"
	"github.com/spdk/spdk-csi/_out/spdk.io/sma/nvmf_tcp"
	csicommon "github.com/spdk/spdk-csi/pkg/csi-common"
	"github.com/spdk/spdk-csi/pkg/util"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	cfgNodePathEnv = "SPDKCSI_NODE_CONFIG"
	cfgNodeIDEnv   = "SPDKCSI_NODE_ID"
)

type nodeServer struct {
	*csicommon.DefaultNodeServer
	mounter     mount.Interface
	volumes     map[string]*nodeVolume
	controllers map[string]int
	mtx         sync.Mutex // protect volumes/controllers maps
	smaClient   sma.StorageManagementAgentClient
	deviceID    string
}
type NodeConfig struct {
	Name            string `json:"name"`
	Subnqn          string `json:"subnqn"`
	TransportAdrfam string `json:"transportAdrfam"`
	TransportType   string `json:"transportType"`
	TransportAddr   string `json:"transportAddr"`
	TransportPort   string `json:"transportPort"`
	SmaGrpcAddr     string `json:"smaGrpcAddr"`
}

var (
	cfgNodePath = ""
	cfgNodeName = ""
	cfgNode     = NodeConfig{}
)

type nodeVolume struct {
	initiator    util.SpdkCsiInitiator
	stagingPath  string
	controllerID string
	tryLock      util.TryLock
}

func init() {
	cfgNodePath = util.FromEnv(cfgNodePathEnv, "/etc/spdkcsi-config/node-config.json")
	cfgNodeName = util.FromEnv(cfgNodeIDEnv, "cnode0")
	klog.Infof("Initializing spdkcsi config file: %s", cfgNodePath)
	err := util.ParseJSONFile(cfgNodePath, &cfgNode)
	if err != nil {
		klog.Warning("Failed to load and parse node server config file. Setting values to defaults.")
		cfgNode = NodeConfig{
			Name:            "localhost",
			Subnqn:          "nqn.2020-04.io.spdk.csi:" + cfgNodeName,
			TransportAdrfam: "ipv4",
			TransportType:   "tcp",
			TransportAddr:   "127.0.0.1",
			TransportPort:   "4421",
			SmaGrpcAddr:     "127.0.0.1:50051",
		}
	} else {
		klog.Infof("Success. Node %s loaded and parsed node server config file.", cfgNodeName)
		cfgNode.Subnqn += cfgNodeName
	}
}

func newNodeServer(d *csicommon.CSIDriver) *nodeServer {
	conn, err := grpc.Dial(cfgNode.SmaGrpcAddr, grpc.WithInsecure())
	if err != nil {
		klog.Fatalln("failed to connect to SMA gRPC server")
	}
	ns := &nodeServer{
		DefaultNodeServer: csicommon.NewDefaultNodeServer(d),
		mounter:           mount.New(""),
		volumes:           make(map[string]*nodeVolume),
		controllers:       make(map[string]int),
		smaClient:         sma.NewStorageManagementAgentClient(conn),
		deviceID:          "",
	}
	err = ns.createDevice()
	if err != nil {
		klog.Fatalln("failed to create a device")
	}
	return ns
}

func (ns *nodeServer) cleanup() {
	err := ns.removeDevice()
	if err != nil {
		klog.Errorf("Node server remove device in cleanup method failed. %s", err.Error())
	}
}

func (ns *nodeServer) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	volume, err := func() (*nodeVolume, error) {
		volumeID := req.GetVolumeId()
		ns.mtx.Lock()
		defer ns.mtx.Unlock()

		volume, exists := ns.volumes[volumeID]
		if !exists {
			controllerID := ""
			var initiatorParams map[string]string
			if strings.EqualFold(req.GetVolumeContext()["targetType"], "tcp") {
				// TODO: this could probably be done under a more fine-grained mutex
				id, volumes, err := ns.connectController(ctx, req.GetVolumeContext())
				if err != nil {
					return nil, err
				}
				controllerID = *id
				ns.controllers[controllerID]++

				ctrlrCleanup := func() {
					if volume == nil {
						if ns.controllers[controllerID] > 0 {
							ns.controllers[controllerID]--
							if ns.controllers[controllerID] == 0 {
								err := ns.disconnectController(ctx, controllerID)
								if err != nil {
									klog.Errorf("Controller (ID=%s) disconnect in NodeStageVolume failed. Error: %s", controllerID, err.Error())
								}
							}
						}
					}
				}
				defer ctrlrCleanup()

				// make sure the volume exists under this controller
				for _, volume := range volumes {
					if volume == volumeID {
						exists = true
						break
					}
				}
				if !exists {
					klog.Errorf("volume %s not found at context %v", volumeID, req.GetVolumeContext())
					return nil, fmt.Errorf("volume not found: %s", volumeID)
				}

				initiatorParams = map[string]string{
					"targetType": cfgNode.TransportType,
					"targetAddr": cfgNode.TransportAddr,
					"targetPort": cfgNode.TransportPort,
					"nqn":        cfgNode.Subnqn,
					"targetPath": req.GetVolumeContext()["targetPath"],
					"model":      req.GetVolumeContext()["model"],
				}
			} else {
				initiatorParams = req.GetVolumeContext()
			}
			initiator, err := util.NewSpdkCsiInitiator(initiatorParams)
			if err != nil {
				return nil, err
			}
			volume = &nodeVolume{
				initiator:    initiator,
				stagingPath:  "",
				controllerID: controllerID,
			}
			ns.volumes[volumeID] = volume
		}
		return volume, nil
	}()
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if volume.tryLock.Lock() {
		defer volume.tryLock.Unlock()

		if volume.stagingPath != "" {
			klog.Warning("volume already staged")
			return &csi.NodeStageVolumeResponse{}, nil
		}

		if volume.controllerID != "" {
			err = ns.attachVolume(ctx, req.GetVolumeId())
			if err != nil {
				return nil, err
			}
		}

		devicePath, err := volume.initiator.Connect() // idempotent
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		stagingPath, err := ns.stageVolume(devicePath, req) // idempotent
		if err != nil {
			volume.initiator.Disconnect() // nolint:errcheck // ignore error
			return nil, status.Error(codes.Internal, err.Error())
		}
		volume.stagingPath = stagingPath
		return &csi.NodeStageVolumeResponse{}, nil
	}
	return nil, status.Error(codes.Aborted, "concurrent request ongoing")
}

func (ns *nodeServer) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	ns.mtx.Lock()
	volume, exists := ns.volumes[volumeID]
	ns.mtx.Unlock()
	if !exists {
		return nil, status.Error(codes.NotFound, volumeID)
	}

	err := func() error {
		if volume.tryLock.Lock() {
			defer volume.tryLock.Unlock()

			if volume.stagingPath == "" {
				klog.Warning("volume already unstaged")
				return nil
			}
			if volume.controllerID != "" {
				err := ns.detachVolume(ctx, volumeID)
				if err != nil {
					return err
				}
			}
			err := ns.deleteMountPoint(volume.stagingPath) // idempotent
			if err != nil {
				return status.Errorf(codes.Internal, "unstage volume %s failed: %s", volumeID, err)
			}
			err = volume.initiator.Disconnect() // idempotent
			if err != nil {
				return status.Error(codes.Internal, err.Error())
			}
			volume.stagingPath = ""
			return nil
		}
		return status.Error(codes.Aborted, "concurrent request ongoing")
	}()
	if err != nil {
		return nil, err
	}

	ns.mtx.Lock()
	if ns.controllers[volume.controllerID] > 0 {
		ns.controllers[volume.controllerID]--
		if ns.controllers[volume.controllerID] == 0 {
			err := ns.disconnectController(ctx, volume.controllerID)
			if err != nil {
				klog.Errorf("Volume controller (ID=%s) disconnect in NodeUnstageVolume failed. Error: %s", volume.controllerID, err.Error())
			}
		}
	}
	delete(ns.volumes, volumeID)
	ns.mtx.Unlock()
	return &csi.NodeUnstageVolumeResponse{}, nil
}

func (ns *nodeServer) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	ns.mtx.Lock()
	volume, exists := ns.volumes[volumeID]
	ns.mtx.Unlock()
	if !exists {
		return nil, status.Error(codes.NotFound, volumeID)
	}

	if volume.tryLock.Lock() {
		defer volume.tryLock.Unlock()

		if volume.stagingPath == "" {
			return nil, status.Error(codes.Aborted, "volume unstaged")
		}
		err := ns.publishVolume(volume.stagingPath, req) // idempotent
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		return &csi.NodePublishVolumeResponse{}, nil
	}
	return nil, status.Error(codes.Aborted, "concurrent request ongoing")
}

func (ns *nodeServer) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	ns.mtx.Lock()
	volume, exists := ns.volumes[volumeID]
	ns.mtx.Unlock()
	if !exists {
		return nil, status.Error(codes.NotFound, volumeID)
	}

	if volume.tryLock.Lock() {
		defer volume.tryLock.Unlock()

		err := ns.deleteMountPoint(req.GetTargetPath()) // idempotent
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		return &csi.NodeUnpublishVolumeResponse{}, nil
	}
	return nil, status.Error(codes.Aborted, "concurrent request ongoing")
}

func (ns *nodeServer) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: []*csi.NodeServiceCapability{
			{
				Type: &csi.NodeServiceCapability_Rpc{
					Rpc: &csi.NodeServiceCapability_RPC{
						Type: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
					},
				},
			},
		},
	}, nil
}

// must be idempotent
func (ns *nodeServer) stageVolume(devicePath string, req *csi.NodeStageVolumeRequest) (string, error) {
	stagingPath := req.GetStagingTargetPath() + "/" + req.GetVolumeId()
	mounted, err := ns.createMountPoint(stagingPath)
	if err != nil {
		return "", err
	}
	if mounted {
		return stagingPath, nil
	}

	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	mntFlags := req.GetVolumeCapability().GetMount().GetMountFlags()

	switch req.VolumeCapability.AccessMode.Mode {
	case csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY:
		mntFlags = append(mntFlags, "ro")
	case csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER:
		return "", errors.New("unsupport MULTI_NODE_MULTI_WRITER AccessMode")
	}

	klog.Infof("mount %s to %s, fstype: %s, flags: %v", devicePath, stagingPath, fsType, mntFlags)
	mounter := mount.SafeFormatAndMount{Interface: ns.mounter, Exec: exec.New()}
	err = mounter.FormatAndMount(devicePath, stagingPath, fsType, mntFlags)
	if err != nil {
		return "", err
	}
	return stagingPath, nil
}

// must be idempotent
func (ns *nodeServer) publishVolume(stagingPath string, req *csi.NodePublishVolumeRequest) error {
	targetPath := req.GetTargetPath()
	mounted, err := ns.createMountPoint(targetPath)
	if err != nil {
		return err
	}
	if mounted {
		return nil
	}

	fsType := req.GetVolumeCapability().GetMount().GetFsType()
	mntFlags := req.GetVolumeCapability().GetMount().GetMountFlags()
	mntFlags = append(mntFlags, "bind")
	klog.Infof("mount %s to %s, fstype: %s, flags: %v", stagingPath, targetPath, fsType, mntFlags)
	return ns.mounter.Mount(stagingPath, targetPath, fsType, mntFlags)
}

// create mount point if not exists, return whether already mounted
func (ns *nodeServer) createMountPoint(path string) (bool, error) {
	unmounted, err := mount.IsNotMountPoint(ns.mounter, path)
	if os.IsNotExist(err) {
		unmounted = true
		err = os.MkdirAll(path, 0755)
	}
	if !unmounted {
		klog.Infof("%s already mounted", path)
	}
	return !unmounted, err
}

// unmount and delete mount point, must be idempotent
func (ns *nodeServer) deleteMountPoint(path string) error {
	unmounted, err := mount.IsNotMountPoint(ns.mounter, path)
	if os.IsNotExist(err) {
		klog.Infof("%s already deleted", path)
		return nil
	}
	if err != nil {
		return err
	}
	if !unmounted {
		err = ns.mounter.Unmount(path)
		if err != nil {
			return err
		}
	}
	return os.RemoveAll(path)
}

func (ns *nodeServer) createDevice() error {
	params, err := anypb.New(&nvmf_tcp.CreateDeviceParameters{
		Subnqn:  &wrapperspb.StringValue{Value: cfgNode.Subnqn},
		Adrfam:  &wrapperspb.StringValue{Value: cfgNode.TransportAdrfam},
		Traddr:  &wrapperspb.StringValue{Value: cfgNode.TransportAddr},
		Trsvcid: &wrapperspb.StringValue{Value: cfgNode.TransportPort},
	})
	if err != nil {
		return err
	}
	response, err := ns.smaClient.CreateDevice(context.Background(),
		&sma.CreateDeviceRequest{
			Type:   &wrapperspb.StringValue{Value: "nvmf_tcp"},
			Params: params,
		})
	if err != nil {
		klog.Error("failed to create a device")
		return err
	}
	klog.Infof("created device: %s", response.Id.Value)
	ns.deviceID = response.Id.Value
	return nil
}

func (ns *nodeServer) removeDevice() error {
	if ns.deviceID == "" {
		return nil
	}

	klog.Infof("removing device: %s", ns.deviceID)
	_, err := ns.smaClient.RemoveDevice(context.Background(),
		&sma.RemoveDeviceRequest{
			Id: &wrapperspb.StringValue{Value: ns.deviceID},
		})
	if err != nil {
		klog.Errorf("failed to remove device: %s", ns.deviceID)
	}
	return err
}

func (ns *nodeServer) connectController(ctx context.Context, ctrlrCtx map[string]string) (*string, []string, error) {
	var controllerType string
	var params *anypb.Any
	var err error

	targetType := strings.ToLower(ctrlrCtx["targetType"])
	switch targetType {
	case "tcp":
		controllerType = "nvmf_tcp"
		params, err = anypb.New(&nvmf_tcp.ConnectControllerParameters{
			Subnqn:  &wrapperspb.StringValue{Value: ctrlrCtx["nqn"]},
			Traddr:  &wrapperspb.StringValue{Value: ctrlrCtx["targetAddr"]},
			Trsvcid: &wrapperspb.StringValue{Value: ctrlrCtx["targetPort"]},
			Adrfam:  &wrapperspb.StringValue{Value: "ipv4"},
		})
	default:
		return nil, nil, fmt.Errorf("unsupported type: %s", targetType)
	}
	if err != nil {
		return nil, nil, err
	}
	response, err := ns.smaClient.ConnectController(ctx,
		&sma.ConnectControllerRequest{
			Type:   &wrapperspb.StringValue{Value: controllerType},
			Params: params,
		})
	if err != nil {
		return nil, nil, err
	}
	var volumes []string
	for _, volume := range response.Volumes {
		volumes = append(volumes, volume.Value)
	}
	klog.Infof("connected controller: %s, volumes: %v", response.Controller.Value, volumes)
	return &response.Controller.Value, volumes, nil
}

func (ns *nodeServer) disconnectController(ctx context.Context, controllerID string) error {
	klog.Infof("disconnecting controller: %s", controllerID)

	_, err := ns.smaClient.DisconnectController(ctx,
		&sma.DisconnectControllerRequest{
			Id: &wrapperspb.StringValue{Value: controllerID},
		})
	if err != nil {
		klog.Errorf("failed to disconnect controller: %s", controllerID)
	}
	return err
}

func (ns *nodeServer) attachVolume(ctx context.Context, volumeGUID string) error {
	klog.Infof("attaching volume: %s to device: %s", volumeGUID, ns.deviceID)

	_, err := ns.smaClient.AttachVolume(ctx,
		&sma.AttachVolumeRequest{
			VolumeGuid: &wrapperspb.StringValue{Value: volumeGUID},
			DeviceId:   &wrapperspb.StringValue{Value: ns.deviceID},
		})
	if err != nil {
		klog.Errorf("failed to attach volume: %s to device: %s", volumeGUID, ns.deviceID)
	}
	return err
}

func (ns *nodeServer) detachVolume(ctx context.Context, volumeGUID string) error {
	klog.Infof("detaching volume: %s", volumeGUID)

	_, err := ns.smaClient.DetachVolume(ctx,
		&sma.DetachVolumeRequest{
			VolumeGuid: &wrapperspb.StringValue{Value: volumeGUID},
			DeviceId:   &wrapperspb.StringValue{Value: ns.deviceID},
		})
	if err != nil {
		klog.Errorf("failed to detach volume: %s", volumeGUID)
	}
	return err
}
