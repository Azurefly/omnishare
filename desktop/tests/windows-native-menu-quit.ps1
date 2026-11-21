#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][int]$DesktopProcessId,
    [Parameter(Mandatory=$true)][string]$ReportPath
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
. "$PSScriptRoot/windows-test-support.ps1"
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Windows.Forms
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class OmniShareNativeMenus {
    [StructLayout(LayoutKind.Sequential)] public struct MouseInput {
        public int X, Y;
        public uint MouseData, Flags, Time;
        public UIntPtr ExtraInfo;
    }
    [StructLayout(LayoutKind.Explicit)] public struct InputData { [FieldOffset(0)] public MouseInput Mouse; }
    [StructLayout(LayoutKind.Sequential)] public struct Input { public uint Type; public InputData Data; }
    [DllImport("user32.dll")] public static extern IntPtr GetMenu(IntPtr window);
    [DllImport("user32.dll")] public static extern int GetMenuItemCount(IntPtr menu);
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern uint SendInput(uint count, Input[] inputs, int size);
    public static bool Click(int x, int y) {
        if (!SetCursorPos(x, y)) return false;
        var inputs = new Input[2];
        inputs[0].Data.Mouse.Flags = 0x0002;
        inputs[1].Data.Mouse.Flags = 0x0004;
        return SendInput(2, inputs, Marshal.SizeOf(typeof(Input))) == 2;
    }
}
'@
$report = [ordered]@{schema_version=1; success=$false; driver='windows-owned-settings-quit-uia'; native_menu=@(); visible_menu_clicks=@()}
$phase = 'owned-window'
function Focus-OwnedWindow {
    param([IntPtr]$Handle, [int]$ProcessId)
    $null = [OmniShareWindowProbe]::ShowWindowAsync($Handle, 9)
    $shell = New-Object -ComObject WScript.Shell
    try {
        $null = $shell.AppActivate($ProcessId)
        $null = [OmniShareWindowProbe]::SetForegroundWindow($Handle)
        Wait-WindowsCondition -Description 'the installed window receiving Settings Quit' -Seconds 5 -Condition {
            [OmniShareWindowProbe]::GetForegroundWindow() -eq $Handle
        }
    } finally { $null = [Runtime.InteropServices.Marshal]::ReleaseComObject($shell) }
}
function Find-VisibleButton {
    param(
        [Windows.Automation.AutomationElement]$Root,
        [string]$Id = '',
        [string]$Name = '',
        [int]$TimeoutSeconds = 45
    )
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $buttonType = [Windows.Automation.ControlType]::Button
    $diag = @()
    do {
        $props = @()
        if ($Id) { $props += ,@([Windows.Automation.AutomationElement]::AutomationIdProperty, $Id) }
        if ($Name) { $props += ,@([Windows.Automation.AutomationElement]::NameProperty, $Name) }
        foreach ($p in $props) {
            $cond = New-Object Windows.Automation.PropertyCondition($p[0], [string]$p[1])
            $elements = $Root.FindAll([Windows.Automation.TreeScope]::Descendants, $cond)
            foreach ($el in $elements) {
                try {
                    $cur = $el.Current
                    $diag += "id=$($cur.AutomationId);name=$($cur.Name);type=$($cur.ControlType.ProgrammaticName);off=$($cur.IsOffscreen);en=$($cur.IsEnabled);w=$($cur.BoundingRectangle.Width);h=$($cur.BoundingRectangle.Height)"
                    if ($cur.ControlType -ne $buttonType) { continue }
                    if ($cur.IsOffscreen) {
                        [object]$scroll = $null
                        if ($el.TryGetCurrentPattern([Windows.Automation.ScrollItemPattern]::Pattern, [ref]$scroll)) {
                            ([Windows.Automation.ScrollItemPattern]$scroll).ScrollIntoView()
                            Start-Sleep -Milliseconds 150
                            $cur = $el.Current
                        }
                    }
                    if (-not $cur.IsOffscreen -and $cur.IsEnabled -and $cur.BoundingRectangle.Width -gt 0 -and $cur.BoundingRectangle.Height -gt 0) {
                        return $el
                    }
                } catch [Windows.Automation.ElementNotAvailableException] { continue }
            }
        }
        Start-Sleep -Milliseconds 200
    } while ([DateTime]::UtcNow -lt $deadline)
    $summary = ($diag | Select-Object -Unique -First 5) -join ' | '
    if (-not $summary) {
        try {
            $all = $Root.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
            $names = @()
            foreach ($el in $all) {
                try {
                    $c = $el.Current
                    if ($c.Name -or $c.AutomationId) { $names += "$($c.ControlType.ProgrammaticName):$($c.AutomationId):$($c.Name)" }
                } catch { continue }
                if ($names.Count -ge 12) { break }
            }
            $summary = "tree($($all.Count))=" + ($names -join ' | ')
        } catch { $summary = 'tree-unavailable' }
    }
    throw "Visible UIA button not found: Id=$Id Name=$Name Diag=[$summary]"
}
function Click-VisibleButton {
    param(
        [Windows.Automation.AutomationElement]$Root,
        [IntPtr]$Handle,
        [int]$ProcessId,
        [string]$Id,
        [string]$Name
    )
    Focus-OwnedWindow -Handle $Handle -ProcessId $ProcessId
    $btn = Find-VisibleButton -Root $Root -Id $Id -Name $Name
    $rect = $btn.Current.BoundingRectangle
    $x = [int][Math]::Floor($rect.X + $rect.Width / 2)
    $y = [int][Math]::Floor($rect.Y + $rect.Height / 2)
    if (-not [OmniShareNativeMenus]::Click($x, $y)) {
        throw "Pointer click failed for button Id=$Id Name=$Name"
    }
    $report.visible_menu_clicks += [ordered]@{label=$Name; left=[int]$rect.Left; top=[int]$rect.Top; right=[int]$rect.Right; bottom=[int]$rect.Bottom}
    return $btn
}
function Invoke-UiaFallback {
    param([Windows.Automation.AutomationElement]$Control)
    $pattern = $null
    if ($Control.TryGetCurrentPattern([Windows.Automation.InvokePattern]::Pattern, [ref]$pattern)) {
        ([Windows.Automation.InvokePattern]$pattern).Invoke()
        return $true
    }
    return $false
}

try {
    $process = Get-Process -Id $DesktopProcessId
    $null = $process.Handle
    $handle = Assert-WindowsVisibleWindow -Process $process
    $menu = [OmniShareNativeMenus]::GetMenu($handle)
    if ($menu -ne [IntPtr]::Zero -and [OmniShareNativeMenus]::GetMenuItemCount($menu) -gt 0) {
        throw 'Windows desktop window must not expose a native menu bar.'
    }
    $root = [Windows.Automation.AutomationElement]::FromHandle($handle)
    Focus-OwnedWindow -Handle $handle -ProcessId $process.Id
    [Windows.Forms.SendKeys]::SendWait('^{HOME}')
    Start-Sleep -Milliseconds 200
    $phase = 'open-settings-dialog'
    $settingsBtn = Click-VisibleButton -Root $root -Handle $handle -ProcessId $process.Id -Id 'settingsBtn' -Name '设置'
    Start-Sleep -Milliseconds 300
    try {
        $null = Find-VisibleButton -Root $root -Id 'quitDesktopBtn' -Name '退出应用' -TimeoutSeconds 2
    } catch {
        $null = Invoke-UiaFallback -Control $settingsBtn
        Start-Sleep -Milliseconds 300
    }

    $phase = 'click-settings-quit-button'
    $quitBtn = Click-VisibleButton -Root $root -Handle $handle -ProcessId $process.Id -Id 'quitDesktopBtn' -Name '退出应用'

    $phase = 'normal-process-exit'
    if (-not $process.WaitForExit(4000)) {
        $null = Invoke-UiaFallback -Control $quitBtn
        if (-not $process.WaitForExit(21000)) { throw 'Settings Quit did not exit the installed process.' }
    }
    if ($process.ExitCode -ne 0) { throw 'Settings Quit returned a nonzero process exit code.' }
    $report.success = $true
} catch {
    $causes = @()
    $cause = $_.Exception
    while ($null -ne $cause -and $causes.Count -lt 6) {
        $causes += [ordered]@{type=$cause.GetType().FullName; hresult=$cause.HResult; message=$cause.Message}
        $cause = $cause.InnerException
    }
    $report.phase = $phase
    $report.exception_chain = $causes
} finally {
    $dir = Split-Path -Parent $ReportPath
    if ($dir) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
    $report | ConvertTo-Json -Depth 6 | Set-Content -Path $ReportPath -Encoding UTF8
}
if (-not $report.success) { exit 1 }
