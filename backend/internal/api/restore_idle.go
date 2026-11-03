package api

import (
	"io"
	"time"
)

// restoreIdleTimeout bounds inactivity during a restore upload. It is a
// variable so tests can shorten it; successful reads reset the timer, so a
// slow-but-alive link is never cut off by a fixed total deadline.
var restoreIdleTimeout = 2 * time.Minute

type idleTimeoutBody struct {
	rc      io.ReadCloser
	timeout time.Duration
	timer   *time.Timer
}

func newIdleTimeoutBody(rc io.ReadCloser, timeout time.Duration) *idleTimeoutBody {
	body := &idleTimeoutBody{rc: rc, timeout: timeout}
	body.timer = time.AfterFunc(timeout, func() {
		// Closing the body makes the blocked Read return an error, which is what
		// lets a dead connection leave multipart parsing.
		_ = rc.Close()
	})
	return body
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if n > 0 && err == nil {
		b.timer.Reset(b.timeout)
	}
	return n, err
}

// Stop cancels the timer without closing the underlying body.
func (b *idleTimeoutBody) Stop() {
	if b.timer != nil {
		b.timer.Stop()
	}
}

func (b *idleTimeoutBody) Close() error {
	b.Stop()
	return b.rc.Close()
}
