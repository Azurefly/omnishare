package main

import "io"

// parentInputClosed is enabled only by a desktop launcher that owns the write
// end of the child's private stdin pipe. EOF (or a broken pipe) enters the same
// graceful shutdown path as SIGTERM, including on Windows without a console.
// Standalone servers never read stdin or depend on it remaining open.
func parentInputClosed(enabled bool, input io.Reader) <-chan struct{} {
	if !enabled {
		return nil
	}
	closed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, input)
		close(closed)
	}()
	return closed
}
