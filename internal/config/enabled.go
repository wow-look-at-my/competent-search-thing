package config

// The affirmative-switch helpers behind the *bool config fields
// (tray.enabled and friends). Split from config.go, which sits at the
// repo's hard line cap (the arbiter.go own-file precedent).

// Bool returns a pointer to v -- the constructor for the affirmative
// *bool switches, whose pointer shape exists so an absent key stays
// distinguishable from an explicit false. A fresh pointer per call;
// callers may write through it.
func Bool(v bool) *bool { return &v }

// Enabled reports the effective value of an affirmative *bool config
// switch: nil -- the key absent from config.json -- means the
// default, which is ON for every pointer-shaped switch in this
// package (the tray.enabled convention; preview.enabled, the one
// default-OFF switch, stays a plain bool where absent = false = the
// default already). Normalize repairs the always-written switches'
// nil pointers to explicit true, so loaded configs carry explicit
// values; this helper keeps directly built configs (tests, zero
// values) and the never-repaired rewrites[].enabled on the same
// absent-means-on semantics.
func Enabled(p *bool) bool { return p == nil || *p }
