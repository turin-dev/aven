# Native Windows host

[한국어](windows-native.ko.md) · [Linux self-hosting](self-hosting.md)

Aven can run as a native Windows executable and Windows service. The supported setup is x64 Windows 10 22H2 (build 19045) or Windows 11 23H2 (build 22631) and later, with Docker Desktop switched to **Linux containers**. Aven itself runs on Windows; Docker Desktop runs the Linux container engine in its configured VM backend.

> Windows Server editions are not supported by this installer. Docker Desktop is not supported on Windows Server, and Aven needs a Linux Docker engine for its proxy and app containers.

## Install

If Docker Desktop is missing, the installer downloads the official x64 installer, verifies its signature, and installs it for all users. It then starts Docker Desktop and waits for the Linux engine. Accept Docker's license agreement in its window if prompted. If Docker Desktop requests a Windows restart, restart and rerun the Aven install command. Run PowerShell as Administrator, then:

```powershell
Invoke-WebRequest https://github.com/turin-dev/aven/releases/latest/download/install-server.ps1 -OutFile .\install-server.ps1
powershell -ExecutionPolicy Bypass -File .\install-server.ps1 -PublicUrl https://aven.example.com -BaseDomain example.com
```

Omit `-PublicUrl` and `-BaseDomain` for a local first run. The installer downloads Aven release assets and verifies their SHA-256 hashes, downloads the Windows Railpack build, creates the `AvenServer` service, and configures restart-on-failure. It keeps settings in `%ProgramData%\Aven\aven.env`, data in `%ProgramData%\Aven\data`, and logs in `%ProgramData%\Aven\logs\aven-server.log`.

The first install generates an admin password unless `-AdminPassword` is supplied. Save the generated password shown at the end. The dashboard is available at the configured public URL; without one, open `http://localhost:7070` on the host.

To install a specific release or upgrade, run the same command with `-Version vX.Y.Z`. Existing settings and data are preserved. Edit `aven.env`, then apply changes with:

```powershell
Restart-Service AvenServer
Get-Content "$env:ProgramData\Aven\logs\aven-server.log" -Wait
```

The installer allows inbound TCP 80 and 443 plus UDP 443 for QUIC, and limits the API, proxy-to-server route, trace receiver, and app-wake ports (7070, 7071, 4318, 5381) to local subnets. If you enable TCP proxying, also allow the configured public port range (20,000–29,999 by default) in Windows Firewall and your network firewall. Configure public DNS for the dashboard and wildcard service domain as described in the [self-hosting guide](self-hosting.md).

## Windows-specific limits

- Workloads must use Linux container images. The Windows service connects to Docker Desktop through its named pipe.
- Windows cannot read the Linux container host's `/proc`; automatic app-port detection is unavailable. Set the service port explicitly in Aven or the app's `PORT` variable.
- Host-level IPv6 assignment, outbound IPv6 enforcement, private mesh, and the Unix-socket edge engine are unavailable on this host.
- Aven's self-hosted container upgrade flow does not apply to the native Windows service. Upgrade the executable with this installer.

For full Linux networking and Windows Server deployments, run Aven on a Linux Docker host.
