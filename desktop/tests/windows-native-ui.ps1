#requires -Version 5.1
[CmdletBinding()]
param(
    [Parameter(Mandatory=$true)][int]$DesktopProcessId,
    [Parameter(Mandatory=$true)][ValidateRange(1024,65535)][int]$AppPort,
    [Parameter(Mandatory=$true)][string]$ReportPath,
    [Parameter(Mandatory=$true)][string]$ScreenshotPath,
    [switch]$FirstRun,
    [string]$StatePath,
    [switch]$AuthenticateOnly,
    [Parameter(Mandatory=$true)][string]$NativeStatePath
)
$ErrorActionPreference = 'Stop'
Set-StrictMode -Version Latest
Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes, System.Windows.Forms, System.Drawing
Add-Type -TypeDefinition @'
using System;
using System.Runtime.InteropServices;
public static class OmniShareNativePointer {
    [StructLayout(LayoutKind.Sequential)] public struct Point { public int X, Y; }
    [StructLayout(LayoutKind.Sequential)] public struct MouseInput {
        public int X, Y;
        public uint MouseData, Flags, Time;
        public UIntPtr ExtraInfo;
    }
    [StructLayout(LayoutKind.Explicit)] public struct InputData {
        [FieldOffset(0)] public MouseInput Mouse;
    }
    [StructLayout(LayoutKind.Sequential)] public struct Input {
        public uint Type;
        public InputData Data;
    }
    [DllImport("user32.dll")] public static extern bool SetCursorPos(int x, int y);
    [DllImport("user32.dll")] public static extern IntPtr WindowFromPoint(Point point);
    [DllImport("user32.dll")] public static extern IntPtr GetAncestor(IntPtr window, uint flags);
    [DllImport("user32.dll", SetLastError=true)] public static extern uint SendInput(uint count, Input[] inputs, int size);
    public static bool Click(int x, int y, IntPtr owner) {
        var point = new Point { X=x, Y=y };
        if (GetAncestor(WindowFromPoint(point), 2) != owner || !SetCursorPos(x, y)) return false;
        var inputs = new Input[2];
        inputs[0].Data.Mouse.Flags = 0x0002;
        inputs[1].Data.Mouse.Flags = 0x0004;
        return SendInput(2, inputs, Marshal.SizeOf(typeof(Input))) == 2;
    }
}
'@
. "$PSScriptRoot/windows-test-support.ps1"
$report = [ordered]@{
    schema_version=1; success=$false; driver='windows-ui-automation-exact-installed-window';
    checks=[ordered]@{}; first_run_requested=[bool]$FirstRun; restart_verification_requested=([bool]$StatePath -and -not $AuthenticateOnly)
}
$phase = 'native-window'
$process = Get-Process -Id $DesktopProcessId
$handle = Assert-WindowsVisibleWindow -Process $process
$root = [Windows.Automation.AutomationElement]::FromHandle($handle)

function Test-ReadOnlyNativeText {
    param($Element)
    # A unique note marker must be rendered outside every editor and search box;
    # an editor's still-present input value is not saved-content evidence.
    $current = $Element
    for ($depth = 0; $null -ne $current -and $depth -lt 32; $depth++) {
        if ($current.Current.ControlType -eq [Windows.Automation.ControlType]::Edit) { return $false }
        if ($current.Equals($root)) { return $true }
        $current = [Windows.Automation.TreeWalker]::ControlViewWalker.GetParent($current)
    }
    return $false
}
function Find-VisibleControl {
    param([string]$Name, [string]$Id='', [Windows.Automation.ControlType[]]$Types=@(), [switch]$ReadOnlyText)
    $process.Refresh()
    if ($process.HasExited) { throw 'The installed native process exited.' }
    # The DOM label and its input share an accessible name. Prefer the exact
    # AutomationId and an input control type, otherwise the label can be chosen
    # and SetFocus fails before the form is ever submitted.
    $properties = @()
    if ($Id) { $properties += ,@([Windows.Automation.AutomationElement]::AutomationIdProperty, $Id) }
    $properties += ,@([Windows.Automation.AutomationElement]::NameProperty, $Name)
    foreach ($property in $properties) {
        $condition = [Windows.Automation.PropertyCondition]::new($property[0], [string]$property[1])
        $elements = $root.FindAll([Windows.Automation.TreeScope]::Descendants, $condition)
        foreach ($element in $elements) {
            try {
                $current = $element.Current
                if ($current.IsOffscreen -and ($Types.Count -eq 0 -or $current.ControlType -in $Types)) {
                    [object]$scroll = $null
                    if ($element.TryGetCurrentPattern([Windows.Automation.ScrollItemPattern]::Pattern, [ref]$scroll)) {
                        ([Windows.Automation.ScrollItemPattern]$scroll).ScrollIntoView()
                        $current = $element.Current
                    }
                }
                if (-not $current.IsOffscreen -and $current.IsEnabled -and
                    $current.BoundingRectangle.Width -gt 0 -and $current.BoundingRectangle.Height -gt 0 -and
                    ($Types.Count -eq 0 -or $current.ControlType -in $Types) -and
                    (-not $ReadOnlyText -or (Test-ReadOnlyNativeText $element))) { return $element }
            } catch [Windows.Automation.ElementNotAvailableException] { continue }
        }
    }
    if ($Name -and $Types.Count -eq 0) {
        # WebView2 exposes the formatted whitespace around note text in Name.
        # Compare normalized visible text, keeping the complete unique marker;
        # raw Name equality would reject content that is visibly on screen.
        $expected = ([regex]::Replace($Name, '\s+', ' ')).Trim()
        $elements = $root.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
        foreach ($element in $elements) {
            try {
                $current = $element.Current
                if (-not $current.IsOffscreen -and $current.BoundingRectangle.Width -gt 0 -and $current.BoundingRectangle.Height -gt 0 -and
                    ([regex]::Replace([string]$current.Name, '\s+', ' ')).Trim() -ceq $expected -and
                    (-not $ReadOnlyText -or (Test-ReadOnlyNativeText $element))) { return $element }
            } catch [Windows.Automation.ElementNotAvailableException] { continue }
        }
    }
    return $null
}
function Await-Control {
    param([string]$Name,[string]$Id='', [Windows.Automation.ControlType[]]$Types=@(), [switch]$ReadOnlyText)
    $end = [DateTime]::UtcNow.AddSeconds(45)
    do {
        try { $item = Find-VisibleControl -Name $Name -Id $Id -Types $Types -ReadOnlyText:$ReadOnlyText }
        catch [Windows.Automation.ElementNotAvailableException] { $item = $null }
        if ($null -ne $item) { return $item }
        Start-Sleep -Milliseconds 300
    } while ([DateTime]::UtcNow -lt $end)
    throw 'A required visible native control did not become ready.'
}
function Set-NativeValue {
    param($Control, [string]$Value)
    $pattern = $null
    if ($Control.TryGetCurrentPattern([Windows.Automation.ValuePattern]::Pattern,[ref]$pattern)) {
        ([Windows.Automation.ValuePattern]$pattern).SetValue($Value)
    } else {
        $Control.SetFocus()
        [Windows.Forms.SendKeys]::SendWait('^a')
        # Credentials stay in memory; neither clipboard nor process arguments.
        $keys = ($Value.ToCharArray() | ForEach-Object {
            if ('+^%~()[]{}'.Contains([string]$_)) { '{' + $_ + '}' } else { [string]$_ }
        }) -join ''
        [Windows.Forms.SendKeys]::SendWait($keys)
    }
}
function Focus-NativeWindow {
    $null = [OmniShareWindowProbe]::ShowWindowAsync($handle, 9)
    $shell = New-Object -ComObject WScript.Shell
    try {
        $null = $shell.AppActivate($process.Id)
        $null = [OmniShareWindowProbe]::SetForegroundWindow($handle)
        Wait-WindowsCondition -Description 'the installed window receiving test input' -Seconds 5 -Condition {
            [OmniShareWindowProbe]::GetForegroundWindow() -eq $handle
        }
    } finally { $null = [Runtime.InteropServices.Marshal]::ReleaseComObject($shell) }
}
function Invoke-NativeButtonClick {
    param([string]$Name, [string]$Id='')
    # A native pointer action also exercises dynamically rendered note buttons.
    # Reacquire after foregrounding; the notes list refreshes every two seconds.
    Focus-NativeWindow
    $control = Await-Control -Name $Name -Id $Id -Types @([Windows.Automation.ControlType]::Button)
    $rect = $control.Current.BoundingRectangle
    $window = $root.Current.BoundingRectangle
    $x = [int][Math]::Floor($rect.X + $rect.Width / 2)
    $y = [int][Math]::Floor($rect.Y + $rect.Height / 2)
    if ($x -lt $window.X -or $x -ge $window.Right -or $y -lt $window.Y -or $y -ge $window.Bottom -or
        [OmniShareWindowProbe]::GetForegroundWindow() -ne $handle -or
        -not [OmniShareNativePointer]::Click($x, $y, $handle)) {
        throw 'The visible native button could not receive an owned-window pointer click.'
    }
}
function Invoke-NativeControl {
    param($Control)
    $pattern = $null
    if ($Control.TryGetCurrentPattern([Windows.Automation.InvokePattern]::Pattern,[ref]$pattern)) {
        ([Windows.Automation.InvokePattern]$pattern).Invoke()
    } else {
        $Control.SetFocus()
        [Windows.Forms.SendKeys]::SendWait('{ENTER}')
    }
}
try {
    Focus-NativeWindow
    $report.checks.visible_owned_native_window = $true
    if ($AuthenticateOnly -and (-not $StatePath -or $FirstRun)) {
        throw 'An authentication-only probe requires private state on an already running window.'
    }
    if ($FirstRun) {
        $phase = 'first-run-form'
        $phase = 'first-run-title'
        $null = Await-Control -Name '首次启动设置' -Id 'title'
        $phase = 'first-run-port-control'
        $portControl = Await-Control -Name '本地服务端口' -Id 'port' -Types @([Windows.Automation.ControlType]::Edit, [Windows.Automation.ControlType]::Spinner)
        $phase = 'first-run-port-value'
        Set-NativeValue $portControl ([string]$AppPort)
        $phase = 'first-run-submit'
        Invoke-NativeControl (Await-Control -Name '保存并启动' -Id 'start' -Types @([Windows.Automation.ControlType]::Button))
        $report.checks.first_run_submit = $true
    }
    if ($StatePath) {
        $phase = 'authentication'
        $private = Get-Content -Raw -Encoding UTF8 -LiteralPath $StatePath | ConvertFrom-Json
        if (-not $private.access_key -or -not $private.note_content) { throw 'Private test state is incomplete.' }
        $field = Await-Control -Name '访问密钥' -Id 'authKey' -Types @([Windows.Automation.ControlType]::Edit)
        Set-NativeValue $field ([string]$private.access_key)
        Invoke-NativeControl (Await-Control -Name '验证并进入' -Id 'authSubmitBtn' -Types @([Windows.Automation.ControlType]::Button))
        $null = Await-Control -Name '设置' -Id 'settingsBtn' -Types @([Windows.Automation.ControlType]::Button)
        $report.checks.authenticated = $true
    }
    if ($StatePath -and -not $AuthenticateOnly) {
        Invoke-NativeControl (Await-Control -Name '随手记' -Types @([Windows.Automation.ControlType]::Button))
        $phase = 'persisted-note'
        Set-NativeValue (Await-Control -Name '搜索随手记' -Id 'noteSearch' -Types @([Windows.Automation.ControlType]::Edit)) ([string]$private.note_content)
        (Await-Control -Name '' -Id 'mainContent').SetFocus()
        [Windows.Forms.SendKeys]::SendWait('^{END}')
        $null = Await-Control -Name ([string]$private.note_content) -ReadOnlyText
        $report.checks.authenticated_after_restart = $true
        $report.checks.persisted_note_visible = $true
        $nativeState = Get-Content -Raw -Encoding UTF8 -LiteralPath $NativeStatePath | ConvertFrom-Json
        if (-not $nativeState.content) { throw 'Native edit state is missing.' }
        $phase = 'native-note-restart'
        Set-NativeValue (Await-Control -Name '搜索随手记' -Id 'noteSearch' -Types @([Windows.Automation.ControlType]::Edit)) ([string]$nativeState.content)
        Focus-NativeWindow
        (Await-Control -Name '' -Id 'mainContent').SetFocus()
        [Windows.Forms.SendKeys]::SendWait('^{END}')
        $null = Await-Control -Name ([string]$nativeState.content) -ReadOnlyText
        $report.checks.native_note_restart_verified = $true
        Set-NativeValue (Await-Control -Name '搜索随手记' -Id 'noteSearch' -Types @([Windows.Automation.ControlType]::Edit)) ''
    }
    $phase = 'business-page'
    foreach ($name in @('随手记','文件','设置')) { $null = Await-Control -Name $name }
    $report.checks.business_page_visible = $true
    if ($FirstRun) {
        $phase = 'native-note-create'
        if (Test-Path -LiteralPath $NativeStatePath) { throw 'Native edit state unexpectedly exists.' }
        Invoke-NativeControl (Await-Control -Name '随手记' -Types @([Windows.Automation.ControlType]::Button))
        $nativeContent = 'native-ui-' + [Guid]::NewGuid().ToString('N')
        $phase = 'native-note-open-dialog'
        Invoke-NativeButtonClick -Name '新建随手记' -Id 'openCreateNoteBtn'
        $phase = 'native-note-composer'
        Set-NativeValue (Await-Control -Name '随手记正文' -Id 'noteContent' -Types @([Windows.Automation.ControlType]::Edit)) $nativeContent
        $phase = 'native-note-publish'
        Invoke-NativeButtonClick -Name '发布随手记' -Id 'createNoteBtn'
        $null = Await-Control -Name '新建随手记' -Id 'openCreateNoteBtn' -Types @([Windows.Automation.ControlType]::Button)
        Start-Sleep -Milliseconds 150
        Focus-NativeWindow
        (Await-Control -Name '' -Id 'mainContent').SetFocus()
        [Windows.Forms.SendKeys]::SendWait('^{END}')
        $phase = 'native-note-created-text'
        $null = Await-Control -Name $nativeContent -ReadOnlyText
        $report.checks.native_note_created = $true
        $phase = 'native-note-edit-open'
        Invoke-NativeButtonClick -Name '编辑'
        $editedContent = $nativeContent + '-edited-through-native-window'
        $phase = 'native-note-edit-value'
        Set-NativeValue (Await-Control -Name '随手记正文' -Id 'editNoteContent' -Types @([Windows.Automation.ControlType]::Edit)) $editedContent
        $phase = 'native-note-edit-save'
        Invoke-NativeControl (Await-Control -Name '保存更新' -Id 'saveEditNoteBtn' -Types @([Windows.Automation.ControlType]::Button))
        $phase = 'native-note-edited-text'
        $null = Await-Control -Name $editedContent -ReadOnlyText
        $report.checks.native_note_edited = $true
        [ordered]@{content=$editedContent} | ConvertTo-Json | Set-Content -LiteralPath $NativeStatePath -Encoding UTF8
    }
    $phase = 'screen-capture'
    $rect = $root.Current.BoundingRectangle
    $bitmap = [Drawing.Bitmap]::new([int]$rect.Width,[int]$rect.Height)
    $graphics = [Drawing.Graphics]::FromImage($bitmap)
    try {
        $graphics.CopyFromScreen([int]$rect.X,[int]$rect.Y,0,0,$bitmap.Size)
        $bitmap.Save([IO.Path]::GetFullPath($ScreenshotPath),[Drawing.Imaging.ImageFormat]::Png)
    } finally { $graphics.Dispose(); $bitmap.Dispose() }
    $report.success = $true
} catch {
    # A provider exception may contain application text. Report only the phase.
    $report.failure = $phase + ': native UI automation failed or timed out'
    $report.exception_type = $_.Exception.GetType().FullName
    $report.exception_hresult = $_.Exception.HResult
    $report.script_line = $_.InvocationInfo.ScriptLineNumber
    $cause = $_.Exception
    $report.exception_chain = @()
    while ($null -ne $cause -and $report.exception_chain.Count -lt 6) {
        $report.exception_chain += [ordered]@{type=$cause.GetType().FullName; hresult=$cause.HResult}
        $cause = $cause.InnerException
    }
    if ($FirstRun -and -not $StatePath) {
        # This is still an isolated empty first-run profile; no credentials or
        # business content have been entered, so a failure image is safe.
        try {
            $rect = $root.Current.BoundingRectangle
            $bitmap = [Drawing.Bitmap]::new([int]$rect.Width,[int]$rect.Height)
            $graphics = [Drawing.Graphics]::FromImage($bitmap)
            try {
                $graphics.CopyFromScreen([int]$rect.X,[int]$rect.Y,0,0,$bitmap.Size)
                $bitmap.Save([IO.Path]::GetFullPath($ScreenshotPath),[Drawing.Imaging.ImageFormat]::Png)
            } finally { $graphics.Dispose(); $bitmap.Dispose() }
        } catch { $report.failure_screenshot_unavailable = $true }
    }
    Write-Host $report.failure
} finally {
    $report | ConvertTo-Json -Depth 6 | Set-Content -LiteralPath $ReportPath -Encoding UTF8
}
if (-not $report.success) { exit 1 }
