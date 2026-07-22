package whatsmeow_service

import (
	"context"
	"errors"
	"testing"
)

type recordingOutboundGuard struct {
	instanceID string
	cost       int
	err        error
}

func (g *recordingOutboundGuard) Wait(_ context.Context, instanceID string, cost int) error {
	g.instanceID = instanceID
	g.cost = cost
	return g.err
}

func (*recordingOutboundGuard) RemoveInstance(string) {}

func TestWaitOutboundDelegatesToSharedGuard(t *testing.T) {
	wantErr := errors.New("limited")
	guard := &recordingOutboundGuard{err: wantErr}
	service := &whatsmeowService{outboundGuard: guard}

	err := service.WaitOutbound(context.Background(), "instance-a", 1)
	if !errors.Is(err, wantErr) {
		t.Fatalf("WaitOutbound error = %v, want %v", err, wantErr)
	}
	if guard.instanceID != "instance-a" || guard.cost != 1 {
		t.Fatalf("guard call = %q/%d", guard.instanceID, guard.cost)
	}
}

func TestWaitOutboundFailsClosedWithoutGuard(t *testing.T) {
	service := &whatsmeowService{}
	if err := service.WaitOutbound(context.Background(), "instance-a", 1); err == nil {
		t.Fatal("missing outbound guard was accepted")
	}
}
