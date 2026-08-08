package modelcatalog

import "testing"

func TestNormalizeEffortValue(t *testing.T) {
	t.Parallel()
	spec := testModelSpec()

	normalized, changed, err := normalizeEffortValue(spec, "xhigh", testModelID)
	if err != nil || !changed || normalized != "high" {
		t.Fatalf("normalizeEffortValue(xhigh) = %q/%v/%v", normalized, changed, err)
	}
	normalized, changed, err = normalizeEffortValue(spec, "high", testModelID)
	if err != nil || changed || normalized != "high" {
		t.Fatalf("normalizeEffortValue(high) = %q/%v/%v", normalized, changed, err)
	}
	if _, _, err := normalizeEffortValue(spec, 3, testModelID); err == nil {
		t.Fatal("non-string effort must be rejected")
	}
	if _, _, err := normalizeEffortValue(spec, "unsupported", testModelID); err == nil {
		t.Fatal("unsupported effort must be rejected")
	}
}
