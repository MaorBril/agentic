package tokens

import "testing"

func TestClampBounds(t *testing.T) {
	for _, tc := range []struct{ in, want float64 }{
		{0.1, MinFactor}, {0.83, 0.83}, {1.0, 1.0}, {9, MaxFactor},
	} {
		if got := Clamp(tc.in); got != tc.want {
			t.Errorf("Clamp(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestCalibrationApply(t *testing.T) {
	c := Calibration{"gpt": 0.8, "junk": 0}
	// Rounds up: correcting the estimate must not cost it its safety bias.
	if got := c.Apply("gpt", 1001); got != 801 {
		t.Errorf("Apply(gpt, 1001) = %d, want 801", got)
	}
	// A model with no measurement, a zero factor, or a nil map is left alone.
	if got := c.Apply("unmeasured", 1000); got != 1000 {
		t.Errorf("uncalibrated model corrected: %d", got)
	}
	if got := c.Apply("junk", 1000); got != 1000 {
		t.Errorf("zero factor applied: %d", got)
	}
	var nilCalib Calibration
	if got := nilCalib.Apply("gpt", 1000); got != 1000 {
		t.Errorf("nil calibration corrected: %d", got)
	}
}

// A wild measurement must not be able to shrink an estimate into a window
// it will not actually fit.
func TestCalibrationApplyClampsOutliers(t *testing.T) {
	c := Calibration{"weird": 0.01}
	if got := c.Apply("weird", 100_000); got != int64(100_000*MinFactor) {
		t.Errorf("Apply = %d, want the clamped %v floor", got, MinFactor)
	}
}
