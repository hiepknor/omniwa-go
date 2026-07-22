package projection_service

import (
	"errors"

	projection_model "github.com/evolution-foundation/evolution-go/pkg/projection/model"
)

const (
	errorCodeProcessingFailed  = "projection_processing_failed"
	errorCodeMisconfigured     = "projection_projector_misconfigured"
	errorCodeUnsupportedEvent  = "projection_event_unsupported"
	errorCodeInvalidPayload    = "projection_payload_invalid"
	errorCodeIncompletePayload = "projection_payload_incomplete"
	errorCodeIdentityMismatch  = "projection_identity_mismatch"
	errorCodeDependencyPending = "projection_dependency_pending"
)

type processingFailure struct {
	Class projection_model.EventFailureClass
	Code  string
}

func (failure *processingFailure) Error() string {
	if failure == nil || failure.Code == "" {
		return errorCodeProcessingFailed
	}
	return failure.Code
}

func permanentProjectionFailure(code string) error {
	return &processingFailure{Class: projection_model.EventFailurePermanent, Code: code}
}

func retryableProjectionFailure(code string) error {
	return &processingFailure{Class: projection_model.EventFailureRetryable, Code: code}
}

func classifyProcessingFailure(err error) (projection_model.EventFailureClass, string) {
	var failure *processingFailure
	if errors.As(err, &failure) && failure != nil && validFailureClass(failure.Class) && validErrorCode(failure.Code) {
		return failure.Class, failure.Code
	}
	return projection_model.EventFailureRetryable, errorCodeProcessingFailed
}

func validFailureClass(class projection_model.EventFailureClass) bool {
	return class == projection_model.EventFailureRetryable || class == projection_model.EventFailurePermanent
}

func validErrorCode(code string) bool {
	return code != "" && len(code) <= 64
}
