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

If a VM with that name already exists, the installer stops and does not start it or change its settings. Re-run with `-UseExisting` only when you mean that VM. `-UseExisting` starts it if it is off and leaves CPU, memory, disk, and automatic start unchanged. If creation fails after `New-VM`, the installer removes that VM and deletes the VHDX only when this run created it. Automatic start is set after the new VM has started.

```powershell
vmconnect.exe localhost "AvenRegion"
```

## 3. Install the Aven region agent

Open a terminal in the Ubuntu VM and run:

```sh
curl -fsSL https://raw.githubusercontent.com/turin-dev/aven/main/install-server.sh | sh
```

Choose **Region server**, enter the main server's HTTPS URL, a one-time join token from *Admin → Server nodes → Add node*, and choose whether to install Docker. Do not pipe this command into `sudo sh`; the installer handles the interactive privilege escalation.

Once the node connects, Aven deploys and manages Docker containers inside the Ubuntu VM. In **Admin → Server nodes**, open that node's actions menu and choose **Connect Hyper-V host**. Run the generated command in elevated PowerShell on Windows Server. That command downloads `aven-hyperv-host.ps1` from this Aven server (`/downloads/aven-hyperv-host.ps1`, the script bundled with the server you are paired to), checks its SHA-256 with `Get-FileHash`, and only then runs it. It does not download the script from GitHub. The host controller stores its credential so that only SYSTEM and Administrators can read it, and the `C:\ProgramData\Aven` directory owner is set to Administrators (or SYSTEM if that fails). VM automatic start is changed only after pairing succeeds. The same menu can then start or gracefully stop the VM. Hyper-V starts the VM again when Windows Server reboots.

Stopping the VM requests a graceful Ubuntu shutdown. Apps deployed to that region are unavailable while the VM is off. A queued power command expires after 120 seconds if the host does not pick it up. A command that stays running for more than 5 minutes is marked failed. Neither case leaves the node blocked, and a stale stop is not run when the host connects again later.

## 4. Remove the host controller

Disconnecting removes Aven's ability to start and stop the VM. It does not delete the Ubuntu VM or the region node.

1. In **Admin → Server nodes**, open the node menu and choose **Disconnect Hyper-V host**. An instance admin can also call `DELETE /api/nodes/{node id}/hyperv`. That revokes the host credential and is written to the audit log. Pairing-token creation and VM start/stop are audited too.
2. On Windows Server, in elevated PowerShell, remove the local controller. The VM is left in place:

```powershell
powershell -ExecutionPolicy Bypass -File "$env:ProgramFiles\Aven\aven-hyperv-host.ps1" -Uninstall -NodeId nod_...
```

`-Uninstall` unregisters the `Aven-HyperV-<node id>` scheduled task and deletes `C:\ProgramData\Aven\HyperV\<node id>` (the credential). If you only disconnect in Aven, the controller gets `401` from the server and, after three rejections in a row, unregisters its own startup task and exits. Run `-Uninstall` as well so the credential file is removed from disk.

## Network

- The Ubuntu VM needs outbound HTTPS access to the main Aven server.
- To serve public domains from this region, point DNS at the VM's public IP or an address forwarded to it. Allow inbound TCP 80 and 443 through upstream and guest firewalls. Allow UDP 443 for QUIC if used.
- If TCP proxying is enabled, forward its configured public port range to the VM (20,000–29,999 by default).
