//go:build !linux

package main

import "errors"

func freeBytes(string) (int64, error) {
	return 0, errors.New("the production disk-reserve check requires Linux")
}
