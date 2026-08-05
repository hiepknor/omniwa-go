package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const (
	DefaultHTTPDrainTimeout   = 30 * time.Second
	DefaultWorkerDrainTimeout = 30 * time.Second
)

type HTTPShutdowner interface {
	Shutdown(context.Context) error
	Close() error
}

// ShutdownActiveServer first removes readiness, then drains in-flight HTTP
// requests, and only then cancels and waits for background workers.
func ShutdownActiveServer(server HTTPShutdowner, runtime *ActiveRuntime, httpTimeout, workerTimeout time.Duration) error {
	if server == nil || runtime == nil || httpTimeout <= 0 || workerTimeout <= 0 {
		return errors.New("server, active runtime, and positive shutdown timeouts are required")
	}
	drainErr := runtime.BeginDrain()

	httpCtx, cancelHTTP := context.WithTimeout(context.Background(), httpTimeout)
	httpErr := server.Shutdown(httpCtx)
	cancelHTTP()
	var closeErr error
	if normalizeShutdownError(httpErr) != nil {
		closeErr = server.Close()
	}

	workerCtx, cancelWorkers := context.WithTimeout(context.Background(), workerTimeout)
	workerErr := runtime.Stop(workerCtx)
	cancelWorkers()

	return errors.Join(
		wrapShutdownError("readiness drain", drainErr),
		wrapShutdownError("HTTP drain", normalizeShutdownError(httpErr)),
		wrapShutdownError("HTTP force close", closeErr),
		wrapShutdownError("worker drain", workerErr),
	)
}

func wrapShutdownError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", phase, err)
}

func normalizeShutdownError(err error) error {
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
