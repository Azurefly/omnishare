package api

import (
	"context"
	"os/exec"
	"strings"
)

func pickExistingFile(ctx context.Context) (string, error) {
	const script = `try
activate
return POSIX path of (choose file with prompt "选择要添加到 OmniShare 的文件")
on error number -128
return ""
end try`
	out, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script).Output()
	return strings.TrimSuffix(string(out), "\n"), err
}
