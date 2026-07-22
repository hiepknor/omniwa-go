package whatsmeow_service

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	instance_model "github.com/evolution-foundation/evolution-go/pkg/instance/model"
	instance_runtime "github.com/evolution-foundation/evolution-go/pkg/instance/runtime"
	"github.com/patrickmn/go-cache"
	"go.mau.fi/whatsmeow"
)

func TestRuntimeTokenReplacementIsRaceSafeAndUpdatesInstance(t *testing.T) {
	client := &MyClient{token: "old-token", Instance: &instance_model.Instance{Token: "old-token"}}
	var wait sync.WaitGroup
	for index := 0; index < 100; index++ {
		wait.Add(2)
		go func() {
			defer wait.Done()
			_ = client.currentToken()
		}()
		go func(value string) {
			defer wait.Done()
			client.replaceToken(value)
		}(fmt.Sprintf("new-token-%d", index))
	}
	wait.Wait()
	if client.currentToken() == "old-token" {
		t.Fatalf("runtime token was not replaced consistently")
	}
}

func TestRuntimeTokenReadDoesNotReenterEventStateLock(t *testing.T) {
	client := &MyClient{token: "token"}
	client.stateMu.Lock()
	done := make(chan string, 1)
	go func() { done <- client.currentToken() }()
	select {
	case token := <-done:
		if token != "token" {
			t.Fatalf("token = %q", token)
		}
	case <-time.After(time.Second):
		t.Fatal("token read blocked on the event state lock")
	}
	client.stateMu.Unlock()
}

func TestUpdateInstanceTokenSynchronizesInstalledRuntimeAndEvictsOldCacheKey(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry := instance_runtime.NewRegistry[*MyClient](ctx)
	client := &MyClient{token: "old-token"}
	if _, err := registry.Install("instance-a", new(whatsmeow.Client), client, nil); err != nil {
		t.Fatal(err)
	}
	userInfoCache := cache.New(cache.NoExpiration, 0)
	userInfoCache.Set("old-token", "cached", cache.NoExpiration)
	service := whatsmeowService{runtimeRegistry: registry, userInfoCache: userInfoCache}

	service.UpdateInstanceToken("instance-a", "new-token")

	if client.currentToken() != "new-token" {
		t.Fatalf("runtime token = %q", client.currentToken())
	}
	if _, found := userInfoCache.Get("old-token"); found {
		t.Fatal("old token cache key survived rotation")
	}
	service.UpdateInstanceToken("not-running", "ignored-token")
}
