package api

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"syscall"
	"unicode/utf16"
)

func pickExistingFile(ctx context.Context) (string, error) {
	const script = `
Add-Type -AssemblyName System.Windows.Forms
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
$dialog = New-Object System.Windows.Forms.OpenFileDialog
try {
    $dialog.Title = 'OmniShare'
    $dialog.CheckFileExists = $true
    if ($dialog.ShowDialog() -eq [System.Windows.Forms.DialogResult]::OK) {
        [Console]::Write($dialog.FileName)
    }
} finally {
    $dialog.Dispose()
}
`
	units := utf16.Encode([]rune(script))
	payload := make([]byte, len(units)*2)
	for i, unit := range units {
		payload[2*i] = byte(unit)
		payload[2*i+1] = byte(unit >> 8)
	}
	command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Sta",
		"-EncodedCommand", base64.StdEncoding.EncodeToString(payload))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := command.Output()
	return strings.TrimSpace(string(out)), err
}
