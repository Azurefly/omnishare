Set-StrictMode -Version Latest

if (-not ('OmniShareWindowProbe' -as [type])) {
    Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class OmniShareWindowProbe {
    [StructLayout(LayoutKind.Sequential)] public struct Rect { public int Left, Top, Right, Bottom; }
    [DllImport("user32.dll")] public static extern bool IsWindowVisible(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern bool GetClientRect(IntPtr hwnd, out Rect rect);
    [DllImport("user32.dll")] public static extern bool ShowWindowAsync(IntPtr hwnd, int command);
    [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr hwnd);
    [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
    [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr hwnd, out uint pid);
}
'@
}

function Wait-WindowsCondition {
    param([scriptblock]$Condition, [string]$Description, [int]$Seconds = 30)
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        if (& $Condition) { return }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for $Description"
}

function Get-WindowsProcessesAtPath {
    param([string]$Executable)
    $path = [IO.Path]::GetFullPath($Executable)
    return @(Get-CimInstance Win32_Process | Where-Object {
        $_.ExecutablePath -and [IO.Path]::GetFullPath($_.ExecutablePath) -eq $path
    })
}

function Get-WindowsFreePort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try { $listener.Start(); return ([Net.IPEndPoint]$listener.LocalEndpoint).Port }
    finally { $listener.Stop() }
}

function Assert-WindowsVisibleWindow {
    param([Diagnostics.Process]$Process)
    Wait-WindowsCondition -Description 'the installed native window' -Condition {
        $Process.Refresh()
        if ($Process.HasExited) { throw 'The installed desktop process exited before showing a window.' }
        $handle = $Process.MainWindowHandle
        return $handle -ne [IntPtr]::Zero -and [OmniShareWindowProbe]::IsWindowVisible($handle)
    }
    $handle = $Process.MainWindowHandle
    [uint32]$owner = 0
    $null = [OmniShareWindowProbe]::GetWindowThreadProcessId($handle, [ref]$owner)
    $rect = [OmniShareWindowProbe+Rect]::new()
    if ($owner -ne $Process.Id -or -not [OmniShareWindowProbe]::GetClientRect($handle, [ref]$rect) -or
        $rect.Right - $rect.Left -lt 400 -or $rect.Bottom - $rect.Top -lt 300 -or
        [OmniShareWindowProbe]::IsIconic($handle)) {
        throw 'The installed desktop does not own a visible, usable native client area.'
    }
    return $handle
}

function Invoke-WindowsNormalQuit {
    param([Diagnostics.Process]$Process)
    $handle = Assert-WindowsVisibleWindow -Process $Process
    $null = [OmniShareWindowProbe]::ShowWindowAsync($handle, 9)
    $shell = New-Object -ComObject WScript.Shell
    try {
        $null = $shell.AppActivate($Process.Id)
        $null = [OmniShareWindowProbe]::SetForegroundWindow($handle)
        Wait-WindowsCondition -Description 'the exact desktop window receiving Quit' -Seconds 5 -Condition {
            [OmniShareWindowProbe]::GetForegroundWindow() -eq $handle
        }
        # Native menu accelerator, not a force kill or a test-only application API.
        Add-Type -AssemblyName System.Windows.Forms
        [Windows.Forms.SendKeys]::SendWait('^q')
        if (-not $Process.WaitForExit(25000)) {
            throw 'Ctrl+Q did not exit the installed desktop within 25 seconds.'
        }
        if ($Process.ExitCode -ne 0) { throw "Normal desktop Quit returned exit code $($Process.ExitCode)." }
    }
    finally { $null = [Runtime.InteropServices.Marshal]::ReleaseComObject($shell) }
}

function Assert-WindowsDebugListener {
    param([int]$Port, [int]$DesktopProcessId)
    Wait-WindowsCondition -Description 'the private loopback WebView debugger' -Condition {
        $listeners = @(Get-NetTCPConnection -State Listen -LocalPort $Port -ErrorAction SilentlyContinue)
        if ($listeners.Count -eq 0) { return $false }
        $processes = @{}
        Get-CimInstance Win32_Process | ForEach-Object { $processes[[int]$_.ProcessId] = $_ }
        foreach ($listener in $listeners) {
            if ($listener.LocalAddress -notin @('127.0.0.1', '::1')) {
                throw 'The test WebView debugger is not restricted to loopback.'
            }
            $current = [int]$listener.OwningProcess
            $visited = @{}
            while ($current -ne $DesktopProcessId -and $processes.ContainsKey($current) -and -not $visited.ContainsKey($current)) {
                $visited[$current] = $true
                $current = [int]$processes[$current].ParentProcessId
            }
            if ($current -ne $DesktopProcessId) { throw 'Debugger listener is not owned by this installed desktop process tree.' }
        }
        return $true
    }
}

function Invoke-WindowsNativeCommand {
    param([string]$Executable, [string[]]$Arguments, [int]$TimeoutSeconds = 180)
    $start = [Diagnostics.ProcessStartInfo]::new()
    $start.FileName = $Executable
    $start.UseShellExecute = $false
    $start.CreateNoWindow = $true
    foreach ($argument in $Arguments) { $start.ArgumentList.Add($argument) }
    $process = [Diagnostics.Process]::Start($start)
    try {
        if (-not $process.WaitForExit($TimeoutSeconds * 1000)) {
            $process.Kill()
            $null = $process.WaitForExit(5000)
            throw 'The installation command exceeded its bounded timeout.'
        }
        return $process.ExitCode
    }
    finally { $process.Dispose() }
}

function Test-WindowsInstallOwnership {
    param([string]$Kind, [object[]]$Registrations, [string]$ProductCode, [string]$InstallDirectory)
    # A partially completed installation may have registered before its command
    # timed out. Never infer ownership merely from DisplayName or a directory.
    if ($Registrations.Count -ne 1) { return $false }
    try {
        $registration = $Registrations[0]
        if ($registration.values.DisplayName -cne 'OmniShare') { return $false }
        $expected = [IO.Path]::GetFullPath($InstallDirectory).TrimEnd('\')
        $location = $registration.values.PSObject.Properties['InstallLocation']
        $registeredPath = if ($location -and $location.Value) { [IO.Path]::GetFullPath(([string]$location.Value).Trim('"').TrimEnd('\')) } else { '' }
        if ($registeredPath -and $registeredPath -ne $expected) { return $false }
        if ($Kind -eq 'msi') {
            # The ProductCode was read from this exact MSI before it was run.
            # InstallLocation can be absent while Windows Installer is rolling
            # back; an exact machine product identity still permits /x cleanup.
            return $ProductCode -match '^\{[0-9A-Fa-f]{8}(-[0-9A-Fa-f]{4}){3}-[0-9A-Fa-f]{12}\}$' -and
                $registration.name -eq $ProductCode -and -not $registration.user
        }
        if ($Kind -ne 'nsis' -or -not $registration.user -or $registration.name -ne 'OmniShare' -or $registeredPath -ne $expected) { return $false }
        $uninstaller = Join-Path $expected 'uninstall.exe'
        $registeredUninstaller = ([string]$registration.values.UninstallString).Trim()
        # Match the complete command, not a prefix containing extra arguments.
        # Execution uses the known path, never an arbitrary registry command.
        return $registeredUninstaller -eq ('"' + $uninstaller + '"') -or $registeredUninstaller -eq $uninstaller
    }
    catch { return $false }
}

function Invoke-WindowsOwnedInstallationCleanup {
    param([string]$Kind, [string]$ProductCode, [string]$InstallDirectory,
        [bool]$CleanPreflight, [bool]$InstallationStarted,
        [scriptblock]$ReadRegistrations, [scriptblock]$Uninstall)
    if (-not $CleanPreflight -or -not $InstallationStarted) { return $false }
    $registrations = @(& $ReadRegistrations)
    if ($registrations.Count -eq 0) { return $false }
    if (-not (Test-WindowsInstallOwnership -Kind $Kind -Registrations $registrations -ProductCode $ProductCode -InstallDirectory $InstallDirectory)) {
        throw 'Refusing cleanup: the remaining product registration cannot be attributed to this installation attempt.'
    }
    $null = & $Uninstall
    return $true
}
