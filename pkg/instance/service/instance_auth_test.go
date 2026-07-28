package instance_service

import (
	"errors"
	"fmt"
	"testing"

	"gorm.io/gorm"
)

func TestClassifyInstanceCredentialError(t *testing.T) {
	infrastructureErr := errors.New("database unavailable")
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: gorm.ErrRecordNotFound, want: ErrInvalidInstanceCredential},
		{name: "wrapped not found", err: fmt.Errorf("lookup instance token: %w", gorm.ErrRecordNotFound), want: ErrInvalidInstanceCredential},
		{name: "infrastructure", err: infrastructureErr, want: infrastructureErr},
		{name: "success"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := classifyInstanceCredentialError(test.err)
			if !errors.Is(got, test.want) || (test.want == nil && got != nil) {
				t.Fatalf("error=%v", got)
			}
		})
	}
}
