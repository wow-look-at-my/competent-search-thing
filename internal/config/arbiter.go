package config

// ArbiterConfig configures learned composition arbitration (see
// internal/arbiter and the README's "Learned arbitration"): a small
// local model, trained on the opt-in search.telemetry pick log, that
// learns which SOURCE a query means -- file, browser tab, or app --
// and re-orders/places only what the deterministic engine already
// delivered. OPT-IN: the zero value keeps the feature entirely off
// (no file reads, no goroutines, byte-identical ordering) -- the
// preview.enabled privacy precedent, because the feature consumes
// behavioral data. Enabling the switch changes nothing user-visible
// until the model also passes its ACTIVATION GATE: at least 200
// recorded picks in the telemetry log plus a holdout accuracy check
// against the delivered order (it must predict the user's picks
// better than what was already shown). The training schedule, gate
// thresholds, and clamp are internal defaults; the switch is the
// whole knob. Lives in its own file: config.go sits at the repo's
// hard line cap.
type ArbiterConfig struct {
	// Enabled turns the learned arbitration layer on.
	Enabled bool `json:"enabled"`
}
