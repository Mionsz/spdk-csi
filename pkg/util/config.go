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

package util

type NodeConfig struct {
	Name            string `json:"name" default:"sma-controller-node"`
	Subnqn          string `json:"subnqn" default:"nqn.2020-04.io.spdk.csi:"`
	TransportAdrfam string `json:"transportAdrfam" default:"ipv4"`
	TransportType   string `json:"transportType" default:"tcp"`
	TransportAddr   string `json:"transportAddr" default:"127.0.0.1"`
	TransportPort   string `json:"transportPort" default:"4421"`
	SmaGrpcAddr     string `json:"smaGrpcAddr" default:"127.0.0.1:50051"`
}

type GlobalConfig struct {
	CfgRPCTimeoutSeconds int    `json:"cfgRPCTimeoutSeconds" default:"20"`
	CfgLvolClearMethod   string `json:"cfgLvolClearMethod" default:"unmap"`
	CfgLvolThinProvision bool   `json:"cfgLvolThinProvision" default:"true"`
	CfgNVMfSvcPort       string `json:"cfgNVMfSvcPort" default:"4420"`
	CfgISCSISvcPort      string `json:"cfgISCSISvcPort" default:"3260"`
	CfgAllowAnyHost      bool   `json:"cfgAllowAnyHost" default:"true"`
	CfgAddrFamily        string `json:"cfgAddrFamily" default:"IPv4"`
}

// Config stores parsed command line parameters
type Config struct {
	DriverName    string
	DriverVersion string
	Endpoint      string
	NodeID        string

	IsControllerServer bool
	IsNodeServer       bool
}

var (
	CfgGlobal = GlobalConfig{}
)
