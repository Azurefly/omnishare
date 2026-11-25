//go:build !windows && !darwin

package api

import (
	"context"
	"errors"
)

func pickExistingFile(context.Context) (string, error) {
	return "", errors.New("请粘贴本机文件的完整路径")
}
