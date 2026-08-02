package instance_credential

import (
	"errors"
	"strings"
	"testing"
)

func TestPrepareNewInstanceTokenGeneratesSecureDefault(t *testing.T) {
	first, err := PrepareNewInstanceToken("")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PrepareNewInstanceToken("")
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 || first == second {
		t.Fatalf("generated credentials are invalid: lengths=%d/%d equal=%t", len(first), len(second), first == second)
	}
}

func TestPrepareNewInstanceTokenValidatesCustomCredential(t *testing.T) {
	valid := strings.Repeat("a", minCustomInstanceTokenBytes)
	prepared, err := PrepareNewInstanceToken(valid)
	if err != nil || prepared != valid {
		t.Fatalf("valid custom credential = %q, %v", prepared, err)
	}

	for _, token := range []string{
		strings.Repeat("a", minCustomInstanceTokenBytes-1),
		strings.Repeat("a", maxInstanceTokenBytes+1),
		strings.Repeat("a", minCustomInstanceTokenBytes-1) + " ",
		strings.Repeat("a", minCustomInstanceTokenBytes-1) + "\n",
		strings.Repeat("a", minCustomInstanceTokenBytes-1) + "é",
	} {
		if _, err := PrepareNewInstanceToken(token); !errors.Is(err, ErrInvalidNewInstanceToken) {
			t.Fatalf("expected invalid token error for %q, got %v", token, err)
		}
	}
}
