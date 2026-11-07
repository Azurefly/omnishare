package api

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// restoreIdleTimeout bounds inactivity during a restore upload. It is a
// variable so tests can shorten it; successful reads reset the timer, so a
// slow-but-alive link is never cut off by a fixed total deadline.
var restoreIdleTimeout = 2 * time.Minute

type idleTimeoutBody struct {
	rc         io.ReadCloser
	timeout    time.Duration
	timer      *time.Timer
	controller *http.ResponseController
	readErr    error
}

func newIdleTimeoutBody(rc io.ReadCloser, timeout time.Duration, w http.ResponseWriter) *idleTimeoutBody {
	body := &idleTimeoutBody{rc: rc, timeout: timeout}
	if w != nil {
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(timeout)); err == nil {
			// A real net/http body cannot be interrupted by Close while Read
			// holds its lock. A connection deadline interrupts the socket read.
			body.controller = controller
			return body
		}
	}
	// Custom readers used outside net/http may not expose a socket deadline.
	body.timer = time.AfterFunc(timeout, func() {
		_ = rc.Close()
	})
	return body
}

func (b *idleTimeoutBody) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	if b.controller != nil {
		if err := b.controller.SetReadDeadline(time.Now().Add(b.timeout)); err != nil {
			return 0, err
		}
	}
	n, err := b.rc.Read(p)
	if err != nil {
		b.readErr = err
	}
	if b.timer != nil && n > 0 && err == nil {
		b.timer.Reset(b.timeout)
	}
	return n, err
}

// Stop cancels the timer without closing the underlying body.
func (b *idleTimeoutBody) Stop() {
	// Only clear a deadline after the complete HTTP body was consumed. Early
	// validation errors and a completed multipart part can leave an unread tail
	// that net/http drains after the handler returns; that drain must be bounded.
	if b.controller != nil && errors.Is(b.readErr, io.EOF) {
		_ = b.controller.SetReadDeadline(time.Time{})
	}
	if b.timer != nil {
		b.timer.Stop()
	}
}

func (b *idleTimeoutBody) Close() error {
	b.Stop()
	return b.rc.Close()
}
