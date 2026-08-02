package main

import (
	"context"
	"net"
	"testing"
)

func TestRunStandbyServerStopsWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runStandbyServer(ctx, "127.0.0.1:0"); err != nil {
		t.Fatalf("runStandbyServer: %v", err)
	}
}

func TestRunStandbyServerFailsWhenAddressIsOwned(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := runStandbyServer(context.Background(), listener.Addr().String()); err == nil {
		t.Fatal("runStandbyServer unexpectedly acquired an owned address")
	}
}
