package projection_service

import (
	"context"
	"errors"
	"time"
)

type WorkerResultHandler func(EventBatchResult, error)

type Worker struct {
	events     EventService
	resource   string
	eventTypes []string
	batchSize  int
	interval   time.Duration
	handler    EventHandler
	onResult   WorkerResultHandler
}

func NewWorker(events EventService, resource string, eventTypes []string, batchSize int, interval time.Duration, handler EventHandler, onResult WorkerResultHandler) *Worker {
	return &Worker{events: events, resource: resource, eventTypes: append([]string(nil), eventTypes...), batchSize: batchSize, interval: interval, handler: handler, onResult: onResult}
}

func (w *Worker) Run(ctx context.Context) error {
	if w == nil || w.events == nil || w.resource == "" || len(w.eventTypes) == 0 || w.batchSize <= 0 || w.interval <= 0 || w.handler == nil {
		return errors.New("projection worker configuration is invalid")
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		result, err := w.events.ProcessBatchFor(ctx, w.resource, w.eventTypes, w.batchSize, w.handler)
		if w.onResult != nil {
			w.onResult(result, err)
		}
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
