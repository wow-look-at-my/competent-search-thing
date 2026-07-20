package config

// ArbiterConfig configures learned composition arbitration (see
// internal/arbiter and the README's "Learned arbitration"): a small
// local model, trained on the local ranking log (search.telemetry),
// that learns which SOURCE a query means -- file, browser tab, or app
// -- and re-orders/places only what the deterministic engine already
// delivered. ON by default (the tray.disabled zero-value-on
// convention): everything is local-only, and the switch being on
// changes nothing user-visible until the model also passes its
// ACTIVATION GATE -- at least 200 recorded picks in the ranking log
// plus a holdout accuracy check against the delivered order (it must
// predict the user's picks better than what was already shown). That
// gate is what makes default-on safe: the model does nothing until it
// provably beats the static order on the user's own data. The
// training schedule, gate thresholds, and clamp are internal
// defaults; the switch is a debug escape hatch, not a privacy
// option. Lives in its own file: config.go sits at the repo's hard
// line cap.
type ArbiterConfig struct {
	// Disabled turns the learned arbitration layer off -- a debug
	// escape hatch for a deterministic ranking baseline, or a kill
	// switch if the learned layer misbehaves. The zero value -- the
	// default -- keeps it on (inert until its activation gate
	// passes).
	Disabled bool `json:"disabled"`
}
