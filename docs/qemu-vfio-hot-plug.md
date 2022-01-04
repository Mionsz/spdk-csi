# SPDK NVMe hot-plug to QEMU with vfio-pci


For purpose of this know-how, I was running:

**Host OS:**   Ubuntu 21 x64 running on VMware host. Full CPU virtualisation enabled. 

**Client OS:** Fedora 31 x64 pre-installed image. 

## **Part I - Configuration**, host (server) tasks

The configuration starts with downloading the SPDK git repository and building it with vfio-user support. To download SPDK git repository and init all submodules dependencies recursively
```bash
$ git clone https://github.com/spdk/spdk ./spdk
$ cd spdk
$ git submodule update --init --recursive
```

Install all dependencies and configure huge pages
```bash
$ scripts/pkgdep.sh --all
$ scripts/setup.sh
```
For Ubuntu distributrions install `apt install libglib2.0-dev`
Enable Linux support for nvme, vfio and iommu by enabling modules and adding them to boot loaded /etc/modules
```bash
$ modprobe nvme_rdma nvme_tcp vfio vfio_pci vfio_iommu_type1 vfio_virqfd
$ cat <<EOF >> /etc/modules
nvme_rdma
nvme_tcp
vfio
vfio_iommu_type1
vfio_pci
vfio_virqfd
EOF
```

Ensure that iommu is boot-enabled and is using pass-through mode (pt). Edit grub boot loader and check existance of parameters

`GRUB_CMDLINE_LINUX_DEFAULT="intel_iommu=on iommu=pt"`

You can also add hugepages pre-config on boot time by adding 

`GRUB_CMDLINE_LINUX_DEFAULT="default_hugepagesz=1G hugepagesz=1G hugepages=4 hugepagesz=2M hugepages=1024 intel_iommu=on iommu=pt"`

For doing so, edit grub (if using one) configuration and recreate grub boot part

```bash
$ vim /etc/default/grub
$ update-grub
$ reboot
```
Check if cmd line parameters have been passed properly using `cat /proc/cmdline`, also check is IOMMU have been enabled by typing `dmesg | grep -i -e DMAR -e IOMMU`

### **Configure and build SPDK with vfio-user support**

From root spdk directory type the following for setting vfio-user enabled, make starts the build process
```bash
./configure --with-vfio-user
make
```

## **Part II - Configuration**, client (QEMU VM) tasks

For testing purposes we wiil use custom, localy build QEMU VM instance with vfio-user native support included. First we need to preconfigure the host by adding packages and enabling kernel modules with load-on-boot assurance. Apt install needed packages
```bash
$ apt-get install qemu-kvm libvirt-daemon-system libvirt-clients bridge-utils cpu-checker
```
Ensure that user `sys_sgci` exists and is in sudoers group. If not then create, add to sudoers and for ease of use remove password.
```
adduser sys_sgci
usermod -aG sudo sys_sgci
passwd --delete sys_sgci
```

Both root and sys_sgci need to be in both `libvirt` and `kvm` groups. Add them if needed
```bash
$ adduser sys_sgci libvirt
$ adduser root libvirt
$ adduser sys_sgci kvm
$ adduser root kvm
```

Check if host machine supports full KVM emulation and if it is configured properly
```bash
$ kvm-ok
```

### **Build QEMU using included SPDK script**

This part of the taks should be executed by sys_sgci user due to scripts compatibility issues. Change user to `sys_sgci` and ensure you are in root-dir of spdk repository. Then start the build process passing both install and update flags.
```bash
$ su sys_sgci
$ ./test/common/config/vm_setup.sh -i -u -t qemu
```

Download intel pre-installed spdk test image from internal network or install it by yourself (password for scp will be on intel MS Teams wiki site). Resize it to meet requirements.
```bash
$ su sys_sgci
$ mkdir -p ~/qemu/image
$ scp sys_sgci@imagebuilder1.igk.intel.com:/var/spdk/vhost/spdk_test_image.qcow2.gz ~/qemu/image
$ gzip -d ~/qemu/image/spdk_test_image.qcow2.gz
$ qemu-img resize /root/qemu/spdk_test_image.qcow2 +20G
$ qemu-img info /root/qemu/spdk_test_image.qcow2
```
No the QEMU VM is ready for booting.

### **One-time QEMU VM fisrt lunch configuration**

After starting the QEMU for the first time you need to make some configuration on a client (VM) side.
Enable Linux hotplug modules for PCI and acpi, resize and apply changes to /dev/sda device
```bash
$ modprobe pci_hotplug
$ modprobe acpiphp
$ growpart /dev/sda 1
$ resize2fs /dev/sda1
```
For manual disk grow use `cfdisk /dev/sda` instead of `growpart`

## **Part III - Run environment**, host tasks

### **Start SPDK NVMf devices.**

From SPDK git root repository folder start two spdk instances
```bash
$ build/bin/nvmf_tgt -r /var/tmp/spdk.sock &
$ build/bin/nvmf_tgt -r /var/tmp/spdk.sock2 &
```

Prepare directory structure for vfio-user-pci transport socket creation. Then create transport with VFIOUSER, malloc block device, create subsytems and add listeners, all using RPC script. All commands should be run from spdk repository root directory
```bash
$ rm -R /var/run/vfio-user
$ mkdir -p /var/run/vfio-user/domain/vfio-user1/1
$ mkdir -p /var/run/vfio-user/domain/vfio-user2/2
$ scripts/rpc.py -s /var/tmp/spdk.sock nvmf_create_transport -t VFIOUSER
$ scripts/rpc.py -s /var/tmp/spdk.sock bdev_malloc_create 64 512 -b Malloc1
$ scripts/rpc.py -s /var/tmp/spdk.sock nvmf_create_subsystem nqn.2019-07.io.spdk:cnode1 -a -s SPDK1
$ scripts/rpc.py -s /var/tmp/spdk.sock nvmf_subsystem_add_ns nqn.2019-07.io.spdk:cnode1 Malloc1
$ scripts/rpc.py -s /var/tmp/spdk.sock nvmf_subsystem_add_listener nqn.2019-07.io.spdk:cnode1 -t VFIOUSER -a "/var/run/vfio-user/domain/vfio-user1/1" -s 0
$ scripts/rpc.py -s /var/tmp/spdk.sock2 nvmf_create_transport -t VFIOUSER
$ scripts/rpc.py -s /var/tmp/spdk.sock2 bdev_malloc_create 64 512 -b Malloc2
$ scripts/rpc.py -s /var/tmp/spdk.sock2 nvmf_create_subsystem nqn.2019-07.io.spdk:cnode2 -a -s SPDK2
$ scripts/rpc.py -s /var/tmp/spdk.sock2 nvmf_subsystem_add_ns nqn.2019-07.io.spdk:cnode2 Malloc2
$ scripts/rpc.py -s /var/tmp/spdk.sock2 nvmf_subsystem_add_listener nqn.2019-07.io.spdk:cnode2 -t VFIOUSER -a "/var/run/vfio-user/domain/vfio-user2/2" -s 0
```

### **Startinng QEMU VM**

To start the VM you need to change working directory to sys_sgci home directory and go to `qemu/vfio-user-v0.93/build/`. From there you can start the VM, using QEMU with vfio user build, do this as a root user due to SPDK limitations. 
```bash
$ su sys_sgci
$ cd ~/qemu/vfio-user-v0.93/build/
```

**Option 1** - To start the machine with PCIe turned off, using old-style machine type=pc, but having PCI hot plug available
```bash
$ sudo ./qemu-system-x86_64 -m 2G -object memory-backend-file,id=mem0,size=2G,mem-path=/dev/hugepages,share=on,prealloc=yes \
	-numa node,memdev=mem0 --enable-kvm \
	-machine type=pc,kernel_irqchip=split,accel=kvm \
	-cpu host -smp cpus=1,maxcpus=12,cores=6,threads=2,sockets=1 -vga std -vnc :100 -net user,hostfwd=tcp::10000-:22 \
	-net nic -drive file=/home/sys_sgci/qemu/image/spdk_test_image.qcow2,if=none,id=os_disk \
	-device ide-hd,drive=os_disk,bootindex=0 -display none \
	-monitor telnet:127.0.0.1:55555,server,nowait \
	-qmp tcp:localhost:55556,server,nowait \
	-device vfio-user-pci,socket=/var/run/vfio-user/domain/vfio-user1/1/cntrl,x-enable-migration=on,id=pre_spdk_nvme_1
```

**Option 2** - To start the machine with new type of controllers, PCIe turned on but PCI hot plug only available by PCIe-to-PCI bridge.
```bash
$ sudo ./qemu-system-x86_64 -m 2G -object memory-backend-file,id=mem0,size=2G,mem-path=/dev/hugepages,share=on,prealloc=yes \
	-numa node,memdev=mem0 --enable-kvm \
	-machine type=q35,kernel_irqchip=split,accel=kvm \
	-cpu host -smp cpus=1,maxcpus=12,cores=6,threads=2,sockets=1 -vga std -vnc :100 -net user,hostfwd=tcp::10000-:22 \
	-net nic -drive file=/home/sys_sgci/qemu/image/spdk_test_image.qcow2,if=none,id=os_disk \
	-device ide-hd,drive=os_disk,bootindex=0 -display none \
	-monitor telnet:127.0.0.1:55555,server,nowait \
	-qmp tcp:localhost:55556,server,nowait \
	-device pcie-pci-bridge,id=spdk_pcie_pci_bridge,bus=pcie.0 \
	-device pci-bridge,id=spdk_pci,bus=spdk_pcie_pci_bridge,chassis_nr=1,addr=0x1 \
	-device vfio-user-pci,socket=/var/run/vfio-user/domain/vfio-user1/1/cntrl,x-enable-migration=on,id=pre_spdk_nvme_1
```

After starting the QEMU VM it can be accessed using localhost connection on port 10000 with password root.
```bash
$ ssh root@127.0.0.1 -p 10000
```
The QEMU monitor can be accessed using telnet
```bash
$ nc -N 127.0.0.1 55555
```

After SSH connecting to QEMU VM test if single NVMe device is detected by the system and NVMe driver
```bash
$ nvme list
```
If everything is ok, up and running try hot-attaching second SPDK instance. Connect to QEMU monitor and pass device_add command
Option 1) For non-bridget instances
```bash
$ device_add vfio-user-pci,socket=/var/run/vfio-user/domain/vfio-user2/2/cntrl,x-enable-migration=on,id=spdk_nvme_2
```
Option 2) For Q35 instance with PCI bridge
```bash
$ device_add vfio-user-pci,socket=/var/run/vfio-user/domain/vfio-user2/2/cntrl,x-enable-migration=on,bus=spdk_pci,id=spdk_nvme_2
```
If no errors were reported, check if new device have been added to the QEMU VM instance. This time you should see two separate NVMe devices
```bash
$ nvme list
```

## **Appendix A** - Setting-up Kubernetes Cluster
==============================================

## **Host OS configuration**

This part is Ubuntu 21 specific. For other OS types refer to Docker and K8s home pages.

For setting up kubernetes controller node the best way is to use included in spdk-csi repository minikube.sh script. For using latest version of kubernetes modules pass values to the config file and run it using `up` switch, but the process of adding more nodes is bit more complicated then doing everything yourself. First of all, we should install all dependencies for multi-node k8s cluster management and initialization.

### **(Ubuntu 21) Docker-ce package install**

```bash
$ apt-get remove docker docker-engine docker.io containerd runc
$ apt-get update
$ apt-get install ca-certificates curl gnupg lsb-release
```

Now add Docker repository, it gpg key, update and install docker.
```bash
$ echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu \
  $(lsb_release -cs) stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
$ curl -fsSL https://download.docker.com/linux/ubuntu/gpg | sudo gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg
$ apt-get update
$ apt-get install docker-ce docker-ce-cli containerd.io
```
To be sure that docker runs as the same cgroup as kubelet

```bash
$ echo "{ \"exec-opts\": [\"native.cgroupdriver=systemd\"] }" >> /etc/docker/daemon.json
$ systemctl daemon-reload
$ systemctl restart docker
$ systemctl enable docker
# Make sure that resolved deamon is started
$ systemctl start systemd-resolved
```
### **(Ubuntu 21) Kubernetes package install**

Update apt and install dependencies

```bash
$ apt-get update
$ apt-get install -y apt-transport-https
```

Download gpg key for repository, add kubernetes native repository and install all needed modules

```bash
$ curl -fsSLo /usr/share/keyrings/kubernetes-archive-keyring.gpg https://packages.cloud.google.com/apt/doc/apt-key.gpg
$ echo "deb [signed-by=/usr/share/keyrings/kubernetes-archive-keyring.gpg] https://apt.kubernetes.io/ kubernetes-xenial main" \
       | sudo tee /etc/apt/sources.list.d/kubernetes.list
$ apt-get update
$ apt-get install -y kubelet kubeadm kubectl
$ apt-mark hold kubelet kubeadm kubectl
$ systemctl start kubelet
$ systemctl enable kubelet
```

### **(Ubuntu 21) Helm package install**

For automating complex tasks and easy installing CNI network manager after deployment Helm needs to be installed

```shell
$ curl https://baltocdn.com/helm/signing.asc | sudo apt-key add -
$ apt-get install apt-transport-https --yes
$ echo "deb https://baltocdn.com/helm/stable/debian/ all main" | sudo tee /etc/apt/sources.list.d/helm-stable-debian.list
$ apt-get update
$ apt-get install helm
$ helm repo add cilium https://helm.cilium.io/
```

### **Starting controller node on host**

If you have old or failed k8s init stage or installation, reset everything using `kubeadm reset` first. Then 
We need to define cluster DNS based endpoint name and probe for controller node default route IP address so that we can add IP HOST_FQDN pair to `/etc/hosts`. This lcuster endpoint name will be `control-node.cluster.private`, and IP=`192.168.109.136` resulting in line appended on every node `192.168.109.136 control-node.cluster.private`

```bash
$ export HOST_CP_ENDPOINT=control-node.cluster.private
$ export HOST_ADDRESS=$(hostname -I | awk '{print $1}')
$ echo "$HOST_ADDRESS $HOST_CP_ENDPOINT" >> /etc/hosts
$ echo "SAVE_ME   >>>$HOST_ADDRESS $HOST_CP_ENDPOINT<<<   SAVE_ME"
# This line will print address name pair. Copy it for later.
# SAVE_ME   >>>192.168.109.136 control-node.cluster.private<<<   SAVE_ME
```

Now begin cluster deployment - by starting server node

```bash
# temporarly disable swap
swapoff -a
export HOST_CP_ENDPOINT=control-node.cluster.private
kubeadm init --pod-network-cidr=10.244.0.0/24 --control-plane-endpoint=$HOST_CP_ENDPOINT --apiserver-advertise-address=0.0.0.0
```

After successfull init, last lines should include kubeadm join cmd for connecting nodes. Copy it and save for later.
```bash
# Copy this line for later use - cluster node join.
kubeadm join control-node.cluster.private:6443 \
    --token tuufjm.jsnuilgiqkk3q6l7 \
    --discovery-token-ca-cert-hash sha256:e3ea477b712def8c801e5806b5f96ebec3259338102cbbf9043e8d45da27260c
```

Copy kubectl config for user access to a cluster using kubectl. Install needed cni plugin.
```bash
$ -p /root/.kube
$ -p /home/ubuntu/.kube
$ cp -i /etc/kubernetes/admin.conf /root/.kube/config
$ cp -i /etc/kubernetes/admin.conf /home/ubuntu/.kube/config
$ chown root:root /root/.kube/config
$ chown ubuntu:ubuntu /home/ubuntu/.kube/config
$ helm install cilium cilium/cilium --namespace=kube-system
```

## **QEMU VM instance OS configuration**

This part if for Fedora 31 OS k8s installation. For other OS types refer to Docker and K8s home pages.

### **Docker-ce package install**

Clean isntall of official  Docker-ce application including containerd driver. First remove all unwanted packages
```bash
$ dnf remove docker docker-client docker-client-latest docker-common docker-latest docker-latest-logrotate docker-logrotate docker-selinux docker-engine-selinux docker-engine
```
Then we can add new, remote repository address for fedora builds of Docker package and all its dependencies.
```bash
$ dnf -y install dnf-plugins-core
$ dnf config-manager --add-repo https://download.docker.com/linux/fedora/docker-ce.repo
$ dnf -y install docker-ce docker-ce-cli containerd.io
$ echo "{ \"exec-opts\": [\"native.cgroupdriver=systemd\"] }" >> /etc/docker/daemon.json
$ systemctl daemon-reload
$ systemctl restart docker
$ systemctl enable docker
# Make sure that resolved deamon is started
$ systemctl start systemd-resolved
```

### **Kubernetes package install**

Add the official kubernetes fedora repository to dnf package manager. The package `exclude` line in script bellow is for auto-update disabling only, so that dnf nor yum will upgrade any of k8s version sensitive packages. 
```bash
$ cat <<EOF | sudo tee /etc/yum.repos.d/kubernetes.repo
[kubernetes]
name=Kubernetes
baseurl=https://packages.cloud.google.com/yum/repos/kubernetes-el7-\$basearch
enabled=1
gpgcheck=1
repo_gpgcheck=1
gpgkey=https://packages.cloud.google.com/yum/doc/yum-key.gpg https://packages.cloud.google.com/yum/doc/rpm-package-key.gpg
exclude=kubelet kubeadm kubectl
EOF
```
It is advised to set SELinux in permissive mode (effectively disabling it) before installing k8s componnents. 
```bash
$ setenforce 0
$ sed -i 's/^SELINUX=enforcing$/SELINUX=permissive/' /etc/selinux/config
$ yum install -y kubelet kubeadm kubectl --disableexcludes=kubernetes
$ systemctl enable --now kubelet
```

Having everything set, we can now join this node to the k8s controller node by running simple commands copied from host steps
```bash
$ echo "192.168.109.136 control-node.cluster.private" >> /etc/hosts
$ swapoff -a
$ kubeadm join control-node.cluster.private:6443 --token tuufjm.jsnuilgiqkk3q6l7 --discovery-token-ca-cert-hash sha256:e3ea477b712def8c801e5806b5f96ebec3259338102cbbf9043e8d45da27260c
```


## **Appendix B** - QEMU VM startup parameters
==============================================

Using QEMU for creating VM instancess supports lots of configuration parameters, some of witch can not be used or were not tested with above workflow (like machine type virt, iommu disabled, no KVM support etc). The purpose of this appendix is to explain parameters used for SPDK with vfio-user-pci tests. The most desired features for this use-case was abbility to hot-plug vfio-user-pci socket backed-up by SPDK driver to running QEMU instance and usage of shared memory, backed-up by huge-pages on host machine. Only having this in mind we can begin the in-depth analysis.

### **QEMU machine type**

QEMU machine type concept simplified, is a way to provide virtual chipset with specific default devices providen (like SATA, PCIe, Ethernet, etc. controllers). There are 3 main types of machine types
1) `pc`   - (pc-i440fx-6.1) Intel i440FX + PIIX chipset, 1996. PCI bus, direct hot-plug
2) `q35`  - (pc-q35-6.1) Intel 82Q35 + ICH9 chipset, 2009. PCIe bus, bridge-based hot-plug
3) `virt` - AArch64 version of ARMv7 architecture. 

For VM speed boost it is advised to use device passthrough by enabling iommu emulation on device and passing the kernel_irqchip param for accelerated irqchip chip support to QEMU

`kernel_irqchip=split`

Adding accelerator param of type kvm for our case. There are other supported accelerators - xen, hax, hvf, whpx or tcg.

`accel=kvm`

Giving final composed parameter passed to QEMU

`-machine type=pc,kernel_irqchip=split,accel=kvm`

### **Setting RAM memory size**

SPDK driver, at its core, requires shared memory access with client OS using hugepages. QEMU on the other side, requires that RAM memory size is equal to pre-allocated memory-backend file. This is why we need to have enought pre-allocated huge pages on host machine for QEMU VM to reserve.
Assign 2G of RAM memory to VM
```
-m 2G
```
Create memory-backend-file based on host huge pages with shared access and pre-allocation
```
-object memory-backend-file,size=2G,mem-path=/dev/hugepages,share=on,prealloc=yes,id=mem0
```
'Bind-attach' memdev with QEMU VM node
```
-numa node,memdev=mem0
```
concatenated to one-line param gives
```
-m 2G -object memory-backend-file,size=2G,mem-path=/dev/hugepages,share=on,prealloc=yes,id=mem0 -numa node,memdev=mem0
```

### **Set the number of CPUs**

Passthrough of host CPU requires enabling kvm and setting cpu to type host, smp param is used for defining number of processors available for VM and it is defined as
```
-smp [cpus=]n[,maxcpus=cpus][,cores=cores][,threads=threads][,dies=dies][,sockets=sockets]
```

For our example we need min. 4 cores (2 reserved by k8s, 1 for docker) but optimal number is 6 or more.
```
-smp cpus=1,maxcpus=12,cores=6,threads=2,sockets=1 -cpu host --enable-kvm
```

### **ONLY FOR TYPE=Q35** - PCIe to PCI bridges definition

When using default machine type for QEMU VM creation, of type q35, there is no possibility of hot-attaching vfio-user-pci due to QEMU limitations. In its core, the Q35 uses native PCIe emulation with no capability of hot-attaching devices to main bus. Workaround for this is passing a runtime parameters describing bridge device from PCIe to PCI (32 free addresses) and attached to it PCI-bridge with desired hot-plugable PCI bus.
Creation of PCIe-to-PCI bridge device attached to VM PCIe.0 bus. This device can handle 32 attached PCI-bridges. Bus name set to spdk_pcie_pci_bridge.
```bash
-device pcie-pci-bridge,bus=pcie.0,id=spdk_pcie_pci_bridge
```
Creation of PCI-bridge device plugged to PCIe-to-PCI bridge device on chassis_nr1. This device can handle 32 attached PCI-devices. Bus name set to spdk_pci.
```bash
-device pci-bridge,bus=spdk_pcie_pci_bridge,chassis_nr=1,addr=0x1,id=spdk_pci
```

Now to hot-plug SPDK vfio-user-pci based device using QEMU interactive monitor you can use `spdk_pci` bus instead of `pcie.0` as follows
```bash
device_add vfio-user-pci,socket=/var/run/vfio-user/domain/vfio-user1/1/cntrl,x-enable-migration=on,bus=spdk_pci,id=spdk_nvme_1
device_add vfio-user-pci,socket=/var/run/vfio-user/domain/vfio-user2/2/cntrl,x-enable-migration=on,bus=spdk_pci,id=spdk_nvme_2
```

### **Block device drive image attach**

```
-drive file=/home/sys_sgci/qemu/image/spdk_test_image.qcow2,if=none,id=os_disk
```

### **Device add based on driver (refferencing drive with id=os_disk)**

```
-device ide-hd,bootindex=0,drive=os_disk
```

### **Disable graphical output and redirect serial I/Os to console**

```
-display none -nographic 
```

### **Monitor access**

Access to QEMU VM monitor for interacting with running instance of VM can be achieved in multiple ways. The two most commonly used are unix-socket and telnet-server based.
For using telnet server listener address and port needs to be specifed as follows:
`-monitor telnet:127.0.0.1:55555,server,nowait`
then to connect with interactive access type
```bash
$ nc -N 127.0.0.1 55555
```

Another way is by setting unix-socket with user-specifed path for socket creation
```
-monitor unix-connect:/tmp/qemu-monitor-socket,server,nowait
```
and connecting to created socket can be done by socat (parameters are for user friendly interaction with no-echo)
```bash
socat -,echo=0,icanon=0 unix-connect:/tmp/qemu-monitor-socket
```

### ** Machine Protocol - QMP over TCP communication**

To use QEMU QMP json based communisation, pass additional parameter when running the VM instance
```
-qmp tcp:localhost:55556,server,nowait
```
Now you should be able to telnet pointed port from inside of host using localhost connection
```bash
$ telnet localhost 55556
```
After connected to QMP server, remember that first and only command you can use is `qmp_capabilities`, type and send one
```bash
S: [...]
C: { "execute": "qmp_capabilities" }
S: {"return": {}}
```
Now you can send other cmds, like query-list available commands:
```bash
C: { "execute": "query-commands" }
S: [...]
```
Device add example using QMP
```bash
C: { "execute": "device_add", 
	 "arguments": { 
   		"driver": "vfio-user-pci", 
   		"socket": "/var/run/vfio-user/domain/vfio-user2/2/cntrl",
   		"x-enable-migration": "on",
   		"bus": "spdk_pci",
   		"id": "spdk_nvme_2"
    }}
S: {"return": {}}
```

## Source list and know-hows:

### SPDK repo and basic
- [BDEV - block device](https://spdk.io/doc/bdev.html)
- [RPC - JSON](https://spdk.io/doc/jsonrpc.html)

### DPDK info:
- [DPDK - Hotplug](https://www.dpdk.org/wp-content/uploads/sites/35/2018/10/am-07-DPDK-hotplug-20180905.pdf)

### libvfio-user repo and know-how:
- [Github repository](https://github.com/nutanix/libvfio-user/tree/master)
- [Know-how - QEMU, SPDK and vfio](https://github.com/nutanix/libvfio-user/blob/master/docs/spdk.md)
- [VFIO User as IPC Protocol in Multi-process QEMU](https://www.youtube.com/watch?v=NBT8rImx3VE)

### QEMU PCIe and hot-add PCI:
- [QEMU PCIe and PCI Hot-plug](https://github.com/qemu/qemu/blob/master/docs/pcie.txt)
- [Q35 machine spec](https://wiki.qemu.org/images/4/4e/Q35.pdf)
- [QEMU PCI vs PCIe](https://wiki.qemu.org/images/f/f6/PCIvsPCIe.pdf)
- [Linux KVM Hot-add PCI](https://www.linux-kvm.org/page/Hotadd_pci_devices)

### QEMU monitor:
- [QEMU monitor wiki](https://en.wikibooks.org/wiki/QEMU/Monitor)
- [QEMU scripting KVM monitor](https://lxadm.com/Scripting_qemu_/_kvm_monitor)

### QEMU monitor QMP (Machine Protocol)
- [QEMU QPC wiki page](https://wiki.qemu.org/Documentation/QMP)
- [QEMU QPC reference manual](https://qemu.readthedocs.io/en/latest/interop/qemu-qmp-ref.html)
