# ssh-bridge

Bridges the Windows native OpenSSH client (`ssh.exe`) to an SSH agent running
inside a WSL2 distro (default: **NixOS**), so the WSL2 agent is the single
source of truth for all SSH keys — no key duplication, no Pageant, no extra
environment variables.

## How it works

```
ssh.exe  ──►  \\.\pipe\openssh-ssh-agent  ──►  ssh-bridge.exe
                                                      │
                                               wsl.exe -d NixOS
                                                      │
                                            socat STDIO UNIX-CONNECT:
                                            /run/user/1000/ssh-agent.sock
                                                      │
                                              WSL2 ssh-agent
```

`ssh-bridge.exe` listens on `\\.\pipe\openssh-ssh-agent` — the exact pipe that
Windows `ssh.exe` queries by default.  For each connection it spawns
`wsl.exe … socat …` and relays raw bytes in both directions.  If WSL2 is
sleeping, `wsl.exe` wakes the distro before socat runs, so no manual
`wsl --exec` is needed beforehand.

## Configuration

Open `main.go` and adjust the two constants at the top:

```go
const (
    wslDistro = "NixOS"                          // wsl -l -v name
    agentSock = "/run/user/1000/ssh-agent.sock"  // path inside the distro
)
```

Make sure **socat** is installed inside the distro:

```bash
# NixOS / nix-env
nix-env -iA nixpkgs.socat

# or add it to your system packages in configuration.nix:
environment.systemPackages = [ pkgs.socat ];
```

## Prerequisites

| Tool | Where |
|------|-------|
| Go ≥ 1.22 | <https://go.dev/dl/> (add to Windows `PATH`) |
| `wsl.exe` | Windows 10 21H2 + / Windows 11 with WSL2 feature enabled |
| `socat` | Inside the WSL2 distro (see above) |
| An SSH agent running inside the distro | e.g. `ssh-agent`, `gpg-agent`, `1password-agent` |

## Build

```powershell
# From the repository root (Windows PowerShell or cmd)
go build -ldflags="-H windowsgui" -o ssh-bridge.exe .
```

`-H windowsgui` prevents a console window from flashing when the bridge starts
at login.  Logs are written to `ssh-bridge.exe.log` in the same directory.

Cross-compile from Linux/macOS:

```bash
GOOS=windows GOARCH=amd64 \
  go build -ldflags="-H windowsgui" -o ssh-bridge.exe .
```

## Install as a Windows Scheduled Task (runs at login, no UAC prompt)

Open **Task Scheduler** → *Create Task* and fill in the fields below, or paste
the PowerShell snippet:

```powershell
# Adjust $exePath to wherever you placed ssh-bridge.exe
$exePath = "C:\Tools\ssh-bridge\ssh-bridge.exe"

$action  = New-ScheduledTaskAction -Execute $exePath
$trigger = New-ScheduledTaskTrigger -AtLogOn
$settings = New-ScheduledTaskSettingsSet `
    -ExecutionTimeLimit ([TimeSpan]::Zero) `
    -RestartCount 3 `
    -RestartInterval (New-TimeSpan -Minutes 1)

Register-ScheduledTask `
    -TaskName  "ssh-bridge" `
    -Action    $action `
    -Trigger   $trigger `
    -Settings  $settings `
    -RunLevel  Highest `
    -Description "Bridge Windows ssh.exe to WSL2 SSH agent"
```

> **Important:** the task must run as your own user account (not SYSTEM) so
> that `wsl.exe` has access to your WSL2 instance.

To start it immediately without logging out:

```powershell
Start-ScheduledTask -TaskName "ssh-bridge"
```

To stop and remove it:

```powershell
Stop-ScheduledTask  -TaskName "ssh-bridge"
Unregister-ScheduledTask -TaskName "ssh-bridge" -Confirm:$false
```

## Verify it works

```powershell
# List keys known to the WSL2 agent via the Windows bridge
ssh-add -l

# Test an actual SSH connection
ssh git@github.com
```

If `ssh-add -l` returns your key fingerprints, the bridge is working.

### Troubleshooting

| Symptom | Likely cause |
|---------|--------------|
| `ssh-add -l` → *error connecting to agent* | `ssh-bridge.exe` is not running — check Task Scheduler |
| `ssh-add -l` → *no identities* | Agent inside WSL2 has no keys loaded — run `ssh-add` inside WSL2 |
| Bridge starts but hangs | `socat` not installed inside the distro, or wrong `agentSock` path |
| Log shows `cmd.Start` error | `wsl.exe` not in `PATH`, or distro name typo in `wslDistro` constant |

Check `ssh-bridge.exe.log` (in the same folder as the binary) for detailed
per-connection logs.

## Security notes

* The Named Pipe ACL (`D:P(A;;GA;;;WD)`) allows **all local users** to connect,
  matching the behaviour of the built-in Windows OpenSSH agent.  Tighten this
  to `(A;;GA;;;OW)` (owner only) if you are on a shared machine.
* Keys never leave WSL2; only the SSH-agent protocol is proxied.
* The bridge does not persist or cache any key material.
