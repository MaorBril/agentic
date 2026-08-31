package tokens

import "math"

// Calibration corrects the character-heuristic Estimate against what
// upstream actually billed, per upstream model id.
//
// Estimate is a 3.5-chars-per-token heuristic plus a 10% margin; measured
// against real tokenizers it runs 15-25% high. That over-count is not
// harmless — it is subtracted from every context budget the router
// checks, so on a 1M-token model it can strand six figures of usable
// window and force needless tier remaps. The router derives a factor per
// model from its own usage log (true input / estimated input over recent
// successful requests) and applies it here.
type Calibration map[string]float64

const (
	// MinFactor / MaxFactor bound the correction. The floor keeps a
	// pathological sample (a session dominated by cache reads, a
	// mis-attributed model id) from shrinking the estimate to something
	// that overflows a real window. The ceiling is deliberately looser
	// than the floor is tight: correcting *upward* restores the
	// safety bias, correcting downward spends it.
	MinFactor = 0.6
	MaxFactor = 1.5

	// MinSamples is how many priced, successful requests a model needs
	// before its measured ratio is trusted. Below this the raw estimate
	// stands.
	MinSamples = 20
)

// Clamp bounds a measured ratio to the trusted correction range.
func Clamp(f float64) float64 {
	switch {
	case f < MinFactor:
		return MinFactor
	case f > MaxFactor:
		return MaxFactor
	default:
		return f
	}
}

// Factor returns the correction for a model, or 1 when uncalibrated.
func (c Calibration) Factor(model string) float64 {
	if c == nil {
		return 1
	}
	if f, ok := c[model]; ok && f > 0 {
		return Clamp(f)
	}
	return 1
}

// Apply corrects a raw estimate for a model. Rounds up, preserving
// Estimate's bias-high property through the correction.
func (c Calibration) Apply(model string, est int64) int64 {
	f := c.Factor(model)
	if f == 1 || est <= 0 {
		return est
	}
	return int64(math.Ceil(float64(est) * f))
}
