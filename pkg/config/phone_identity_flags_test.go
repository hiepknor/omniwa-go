package config

import "testing"

func TestPhoneNumberExposureRequiresEvidenceCollection(t *testing.T) {
	for _, test := range []struct{ evidence, exposure, valid bool }{
		{false, false, true}, {true, false, true}, {true, true, true}, {false, true, false},
	} {
		err := validatePhoneIdentityFeatureFlags(test.evidence, test.exposure)
		if (err == nil) != test.valid {
			t.Fatalf("evidence=%v exposure=%v err=%v", test.evidence, test.exposure, err)
		}
	}
}
