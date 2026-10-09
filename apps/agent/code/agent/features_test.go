package agent

import (
	"slices"
	"testing"
)

func TestDefaultDisabledKeepsTheVerifyGateMicroAndCheckpoint(t *testing.T) {
	if err := checkFeatures(DefaultDisabled); err != nil {
		t.Fatal(err)
	}
	for _, on := range []string{FeatureBaseline, FeatureDiffReview, FeatureTestEdits, FeatureMicro, FeatureCheckpoint, FeatureProgress, FeatureBudget} {
		if slices.Contains(DefaultDisabled, on) {
			t.Errorf("%s is off by default", on)
		}
	}
}
