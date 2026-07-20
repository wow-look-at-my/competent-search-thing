package app

import (
	"bytes"
	"log"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// fpsTestApp returns a newTestApp with the meter env knob set (the
// getenv seam pins every other variable to "").
func fpsTestApp(t *testing.T) *App {
	t.Helper()
	a, _ := newTestApp(t, nil, Options{})
	a.plat.getenv = func(k string) string {
		if k == EnvFPSMeter {
			return "1"
		}
		return ""
	}
	return a
}

func validFPSSample() FPSSample {
	return FPSSample{AvgFPS: 59.8, MaxFPS: 118.9, LongFramePct: 2, WindowMS: 5000, Frames: 299, InferredHz: 120}
}

func TestFPSEnabledOffByDefault(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{}) // getenv pinned to ""
	require.False(t, a.FPSEnabled())
	require.True(t, fpsTestApp(t).FPSEnabled())
}

func TestRecordFPSSampleDisabledIsSilentNoOp(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	// Even a garbage sample earns no error and no log while the meter
	// is off -- the frontend cannot probe or spam through this method.
	require.NoError(t, a.RecordFPSSample(FPSSample{AvgFPS: math.NaN()}))
	require.Empty(t, buf.String())
}

func TestRecordFPSSampleLogFormat(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a := fpsTestApp(t)
	require.NoError(t, a.RecordFPSSample(validFPSSample()))
	require.Contains(t, buf.String(),
		"fps: 59.8 avg, 118.9 max, 2% frames >20ms over 5.0s (rAF ~120Hz)")
}

func TestRecordFPSSampleValidation(t *testing.T) {
	a := fpsTestApp(t)
	mutate := func(f func(*FPSSample)) FPSSample {
		s := validFPSSample()
		f(&s)
		return s
	}
	cases := []struct {
		name string
		s    FPSSample
	}{
		{"nan avg", mutate(func(s *FPSSample) { s.AvgFPS = math.NaN() })},
		{"inf max", mutate(func(s *FPSSample) { s.MaxFPS = math.Inf(1) })},
		{"negative avg", mutate(func(s *FPSSample) { s.AvgFPS = -1 })},
		{"avg too big", mutate(func(s *FPSSample) { s.AvgFPS = 1001 })},
		{"pct negative", mutate(func(s *FPSSample) { s.LongFramePct = -1 })},
		{"pct over 100", mutate(func(s *FPSSample) { s.LongFramePct = 101 })},
		{"window too small", mutate(func(s *FPSSample) { s.WindowMS = 50 })},
		{"window too big", mutate(func(s *FPSSample) { s.WindowMS = 70000 })},
		{"zero frames", mutate(func(s *FPSSample) { s.Frames = 0 })},
		{"frames too big", mutate(func(s *FPSSample) { s.Frames = 200000 })},
		{"hz negative", mutate(func(s *FPSSample) { s.InferredHz = -1 })},
		{"hz too big", mutate(func(s *FPSSample) { s.InferredHz = 1001 })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Error(t, a.RecordFPSSample(c.s))
		})
	}
	require.NoError(t, a.RecordFPSSample(validFPSSample()))
}

func TestStartFPSInfoDisabledLogsNothing(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	armed := false
	a.plat.watchPowerChanges = func(func()) bool { armed = true; return true }
	a.startFPSInfo()
	require.Empty(t, buf.String())
	require.False(t, armed, "the observer must not arm while the meter is off")
}

func TestStartFPSInfoLogsContextAndChanges(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a := fpsTestApp(t)
	lowPower := false
	a.plat.powerInfo = func() (platform.PowerInfo, bool) {
		return platform.PowerInfo{MaxFPS: 120, LowPowerMode: lowPower, ThermalState: 0}, true
	}
	var fire func()
	a.plat.watchPowerChanges = func(onChange func()) bool {
		fire = onChange
		return true
	}
	a.startFPSInfo()
	require.Contains(t, buf.String(),
		"fps: meter on; display 120Hz max, lowPowerMode=off, thermalState=nominal")
	require.NotNil(t, fire, "the change observer is armed")

	lowPower = true
	fire()
	require.Contains(t, buf.String(),
		"fps: power state changed: display 120Hz max, lowPowerMode=on, thermalState=nominal")
}

func TestPowerInfoLineDegradesHonestly(t *testing.T) {
	a := fpsTestApp(t)
	// Nil seam (linux/windows): named as such.
	require.Equal(t, "display power info unavailable on this platform", a.powerInfoLine())
	// Probe present but failing.
	a.plat.powerInfo = func() (platform.PowerInfo, bool) { return platform.PowerInfo{}, false }
	require.Equal(t, "display power info unavailable", a.powerInfoLine())
}

func TestApplyNear60UncapAppliesOnceAndLogs(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	calls := 0
	a.plat.uncapNear60 = func() platform.UncapStatus {
		calls++
		return platform.UncapApplied
	}
	a.applyNear60Uncap(false)
	a.applyNear60Uncap(true) // already latched: no second SPI call
	require.Equal(t, 1, calls)
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("near-60 cap disabled")))
}

func TestApplyNear60UncapTransientMissStaysQuietUntilFinal(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	st := platform.UncapNoWindow
	a.plat.uncapNear60 = func() platform.UncapStatus { return st }
	a.applyNear60Uncap(false)
	require.Empty(t, buf.String(), "a transient miss on the early attempt is silent")
	st = platform.UncapSPIMissing
	a.applyNear60Uncap(true)
	require.Contains(t, buf.String(),
		"fps: WebKit near-60 uncap unavailable (WKPreferences feature SPI unavailable)")
}

func TestApplyNear60UncapEscapeHatch(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)

	a, _ := newTestApp(t, nil, Options{})
	a.plat.getenv = func(k string) string {
		if k == EnvKeepNear60 {
			return "1"
		}
		return ""
	}
	called := false
	a.plat.uncapNear60 = func() platform.UncapStatus { called = true; return platform.UncapApplied }
	a.applyNear60Uncap(false)
	require.False(t, called, "the escape hatch skips the SPI entirely")
	require.Contains(t, buf.String(), "fps: WebKit near-60 uncap skipped (COMPETENT_SEARCH_KEEP_NEAR60=1)")
	// Latched: the DomReady attempt neither calls nor re-logs.
	a.applyNear60Uncap(true)
	require.False(t, called)
	require.Equal(t, 1, bytes.Count(buf.Bytes(), []byte("uncap skipped")))
}

func TestApplyNear60UncapNilSeamNoOp(t *testing.T) {
	a, _ := newTestApp(t, nil, Options{}) // uncapNear60 nil (linux)
	a.applyNear60Uncap(false)
	a.applyNear60Uncap(true) // must not panic or log
}
