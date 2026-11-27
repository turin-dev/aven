# Hyper-V region node on Windows Server

Use Windows Server as the Hyper-V host. Run the Aven region agent and app containers inside an Ubuntu VM. Keep the main Aven server on its existing host.

## 1. Prepare Hyper-V

In an elevated PowerShell session, install the Hyper-V role. Windows Server restarts when installation completes.

```powershell
Install-WindowsFeature -Name Hyper-V -IncludeManagementTools -Restart
```

In Hyper-V Manager, create an **External** virtual switch connected to the physical network adapter. The VM needs to reach the main Aven server and receive region app traffic. If Windows Server itself is a cloud VM, the provider must allow nested virtualization and the required external networking.

## 2. Create the Ubuntu VM

Download and run the installer in elevated PowerShell:

```powershell
Invoke-WebRequest https://github.com/turin-dev/aven/releases/latest/download/install-region-vm.ps1 -OutFile .\install-region-vm.ps1
powershell -ExecutionPolicy Bypass -File .\install-region-vm.ps1
```

The interactive installer asks for a VM name, vCPU count, memory, and disk size. It uses the available external switch, prompting you to choose if there is more than one. It downloads the official Ubuntu Server 24.04 LTS ISO, verifies its SHA-256 checksum, creates the Hyper-V VM, and configures it to start when Windows boots. Finish the Ubuntu installation in VMConnect.

```powershell
vmconnect.exe localhost "AvenRegion"
```

## 3. Install the Aven region agent

Open a terminal in the Ubuntu VM and run:

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sh
```

Choose **Region server**, enter the main server's HTTPS URL, a one-time join token from *Admin → Server nodes → Add node*, and choose whether to install Docker. Do not pipe this command into `sudo sh`; the installer handles the interactive privilege escalation.

Once the node connects, Aven deploys and manages Docker containers inside the Ubuntu VM. In **Admin → Server nodes**, open that node's actions menu and choose **Connect Hyper-V host**. Run the generated command in elevated PowerShell on Windows Server to install the host controller. The same menu can then start or gracefully stop the VM. Hyper-V starts the VM again when Windows Server reboots.

Stopping the VM requests a graceful Ubuntu shutdown. Apps deployed to that region are unavailable while the VM is off.

## Network

- The Ubuntu VM needs outbound HTTPS access to the main Aven server.
- To serve public domains from this region, point DNS at the VM's public IP or an address forwarded to it. Allow inbound TCP 80 and 443 through upstream and guest firewalls. Allow UDP 443 for QUIC if used.
- If TCP proxying is enabled, forward its configured public port range to the VM (20,000–29,999 by default).
