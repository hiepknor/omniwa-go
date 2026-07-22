package projection_service

import (
	"errors"
	"strings"
	"testing"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

func TestProcessingFailureClassificationDefaultsSafely(t *testing.T) {
	class, code := classifyProcessingFailure(errors.New("provider response containing sensitive data"))
	if class != projection_model.EventFailureRetryable || code != errorCodeProcessingFailed {
		t.Fatalf("generic classification=%q/%q", class, code)
	}
	class, code = classifyProcessingFailure(permanentProjectionFailure(errorCodeInvalidPayload))
	if class != projection_model.EventFailurePermanent || code != errorCodeInvalidPayload {
		t.Fatalf("permanent classification=%q/%q", class, code)
	}
	class, code = classifyProcessingFailure(&processingFailure{Class: projection_model.EventFailurePermanent, Code: strings.Repeat("x", 65)})
	if class != projection_model.EventFailureRetryable || code != errorCodeProcessingFailed {
		t.Fatalf("invalid safe code classification=%q/%q", class, code)
	}
}
