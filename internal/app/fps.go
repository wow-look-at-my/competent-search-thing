package app

import (
	"fmt"
	"log"
	"math"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// EnvFPSMeter enables the dev-only fps meter (COMPETENT_SEARCH_FPS=1):
// the frontend runs a requestAnimationFrame loop while the bar is
// visible and reports periodic summaries through RecordFPSSample,
// while the Go side logs one display/power context line at startup
// (and on every power-state change, macOS only). Off -- the default --
// nothing registers anywhere: zero cost. Documented in the README
// beside the other env knobs.
const EnvFPSMeter = "COMPETENT_SEARCH_FPS"

// EnvKeepNear60 opts out of the WebKit near-60 uncap
// (COMPETENT_SEARCH_KEEP_NEAR60=1): the escape hatch for the one
// private-API touch, in case a macOS update misbehaves with the
// feature flipped. Independent of the fps meter.
const EnvKeepNear60 = "COMPETENT_SEARCH_KEEP_NEAR60"

// FPSSample is one frontend meter report (fpsmeter.ts summarize; field
// names lockstep with wails.d.ts FPSSample). All figures describe one
// accumulated window of VISIBLE frame time.
type FPSSample struct {
	// AvgFPS is frames / window: the number the "feels like 30fps"
	// report is about.
	AvgFPS float64 `json:"avgFps"`
	// MaxFPS is 1s / the smallest frame delta -- the fastest the loop
	// ever ran, evidence of the achievable rate.
	MaxFPS float64 `json:"maxFps"`
	// LongFramePct is the percentage of frames over 20ms (a steady
	// 30fps throttle reads ~100% -- deliberately loud).
	LongFramePct int `json:"longFramePct"`
	// WindowMS is the accumulated visible time summarized (ms).
	WindowMS int `json:"windowMs"`
	// Frames is the number of frame deltas in the window.
	Frames int `json:"frames"`
	// InferredHz is the rAF cadence estimate (10th-percentile delta
	// inverted, snapped to common panel rates) -- compared against the
	// Go context line's "display NHz max", the diagnosis in one pair.
	InferredHz int `json:"inferredHz"`
}

// FPSEnabled is bound to the frontend: whether the dev-only fps meter
// is on. The frontend calls it once at wire-up and registers NOTHING
// when it answers false.
func (a *App) FPSEnabled() bool {
	return a.plat.getenv(EnvFPSMeter) == "1"
}

// RecordFPSSample is bound to the frontend: it logs one meter report
// in the repo's inline-metrics format. Defense in depth like
// RecordPick: the echoed payload is re-validated before anything
// reaches the log, and with the meter disabled the call is a silent
// no-op -- the frontend cannot make the Go side log spam without the
// env knob set.
func (a *App) RecordFPSSample(s FPSSample) error {
	if !a.FPSEnabled() {
		return nil
	}
	if err := validateFPSSample(s); err != nil {
		return err
	}
	log.Printf("fps: %.1f avg, %.1f max, %d%% frames >20ms over %.1fs (rAF ~%dHz)",
		s.AvgFPS, s.MaxFPS, s.LongFramePct, float64(s.WindowMS)/1000, s.InferredHz)
	return nil
}

// validateFPSSample bounds every echoed field: finite rates in
// 0..1000, a real percentage, a window between the meter's first
// report (~2.5s) floor and a minute, and sane frame counts.
func validateFPSSample(s FPSSample) error {
	for _, f := range []struct {
		name string
		v    float64
	}{{"avgFps", s.AvgFPS}, {"maxFps", s.MaxFPS}} {
		if math.IsNaN(f.v) || math.IsInf(f.v, 0) {
			return fmt.Errorf("fps sample: %s is not finite", f.name)
		}
		if f.v < 0 || f.v > 1000 {
			return fmt.Errorf("fps sample: %s %.1f outside 0..1000", f.name, f.v)
		}
	}
	if s.LongFramePct < 0 || s.LongFramePct > 100 {
		return fmt.Errorf("fps sample: longFramePct %d outside 0..100", s.LongFramePct)
	}
	if s.WindowMS < 100 || s.WindowMS > 60000 {
		return fmt.Errorf("fps sample: windowMs %d outside 100..60000", s.WindowMS)
	}
	if s.Frames < 1 || s.Frames > 100000 {
		return fmt.Errorf("fps sample: frames %d outside 1..100000", s.Frames)
	}
	if s.InferredHz < 0 || s.InferredHz > 1000 {
		return fmt.Errorf("fps sample: inferredHz %d outside 0..1000", s.InferredHz)
	}
	return nil
}

// startFPSInfo runs once at Startup: with the meter enabled it logs
// the display/power context line the frontend's fps summaries are read
// against -- "display 120Hz max, lowPowerMode=off" names WebKit's
// active throttle in one glance -- and arms the power-state change
// observer (macOS; the seams are nil elsewhere) so flips are logged
// the moment they happen.
func (a *App) startFPSInfo() {
	if !a.FPSEnabled() {
		return
	}
	log.Printf("fps: meter on; %s", a.powerInfoLine())
	if a.plat.watchPowerChanges != nil {
		a.plat.watchPowerChanges(func() {
			log.Printf("fps: power state changed: %s", a.powerInfoLine())
		})
	}
}

// powerInfoLine renders the current display/power state for the fps
// meter's context lines; platforms without a probe (the seam is nil
// off darwin) say so honestly.
func (a *App) powerInfoLine() string {
	if a.plat.powerInfo == nil {
		return "display power info unavailable on this platform"
	}
	pi, ok := a.plat.powerInfo()
	if !ok {
		return "display power info unavailable"
	}
	return fmt.Sprintf("display %dHz max, lowPowerMode=%s, thermalState=%s",
		pi.MaxFPS, onOff(pi.LowPowerMode), platform.ThermalStateString(pi.ThermalState))
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

// applyNear60Uncap flips WebKit's PreferPageRenderingUpdatesNear60FPS
// feature OFF through the guarded SPI (the seam is nil off darwin), so
// ProMotion panels render at their real refresh rate -- and, since the
// Low Power Mode throttle HALVES the nominal rate, 120Hz panels keep
// >= 60fps even under LPM. Default ON per the "never below 60" report;
// EnvKeepNear60=1 is the escape hatch. Attempted at Startup (earliest
// possible -- the webview already exists) and again at DomReady;
// transient misses (no window/webview yet) stay quiet until the FINAL
// attempt, and success or a permanent failure logs exactly once.
func (a *App) applyNear60Uncap(final bool) {
	if a.plat.uncapNear60 == nil {
		return
	}
	a.mu.Lock()
	done := a.uncapDone
	a.mu.Unlock()
	if done {
		return
	}
	if a.plat.getenv(EnvKeepNear60) == "1" {
		a.setUncapDone()
		log.Printf("fps: WebKit near-60 uncap skipped (%s=1)", EnvKeepNear60)
		return
	}
	st := a.plat.uncapNear60()
	switch {
	case st == platform.UncapApplied:
		a.setUncapDone()
		log.Printf("fps: WebKit near-60 cap disabled (rendering follows the display's refresh rate)")
	case final:
		a.setUncapDone()
		log.Printf("fps: WebKit near-60 uncap unavailable (%s); content stays at <= 60fps on >60Hz panels", st)
	}
}

func (a *App) setUncapDone() {
	a.mu.Lock()
	a.uncapDone = true
	a.mu.Unlock()
}
