package projection_service

import (
	"context"
	"testing"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

type phoneResolverRepositoryStub struct {
	instanceID string
	resolved   map[string]string
	rows       []projection_model.PhoneIdentityEvidence
	err        error
}

func (s *phoneResolverRepositoryStub) Observe(context.Context, projection_model.PhoneIdentityEvidence) (bool, error) {
	return false, s.err
}
func (s *phoneResolverRepositoryStub) Resolve(_ context.Context, instanceID string, _ []string) (map[string]string, error) {
	s.instanceID = instanceID
	return s.resolved, s.err
}
func (s *phoneResolverRepositoryStub) List(_ context.Context, instanceID string) ([]projection_model.PhoneIdentityEvidence, error) {
	s.instanceID = instanceID
	return s.rows, s.err
}

func TestPhoneNumberResolverUsesOnlyInstanceScopedEvidence(t *testing.T) {
	repository := &phoneResolverRepositoryStub{resolved: map[string]string{"123@lid": "15550001@s.whatsapp.net"}}
	resolver := NewPhoneNumberResolver(repository, true, nil)
	result := resolver.Resolve(context.Background(), "11111111-1111-1111-1111-111111111111", []string{"123@lid"})
	if repository.instanceID != "11111111-1111-1111-1111-111111111111" || result["123@lid"] != "15550001" {
		t.Fatalf("result=%#v instance=%s", result, repository.instanceID)
	}
}

func TestPhoneNumberResolverDisabledPreservesOmission(t *testing.T) {
	repository := &phoneResolverRepositoryStub{resolved: map[string]string{"123@lid": "15550001@s.whatsapp.net"}}
	if result := NewPhoneNumberResolver(repository, false, nil).Resolve(context.Background(), "instance-a", []string{"123@lid"}); len(result) != 0 || repository.instanceID != "" {
		t.Fatalf("result=%#v instance=%s", result, repository.instanceID)
	}
}

func TestPhoneDigitsSupportsLegacyAndDeviceJIDs(t *testing.T) {
	for input, expected := range map[string]string{"15550001:7@s.whatsapp.net": "15550001", "15550002@c.us": "15550002", "+1555@s.whatsapp.net": ""} {
		if actual := phoneDigits(input); actual != expected {
			t.Fatalf("phoneDigits(%q)=%q want %q", input, actual, expected)
		}
	}
}
