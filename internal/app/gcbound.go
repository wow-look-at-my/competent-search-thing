package app

// The initial-build GC bound. The whole-filesystem walk allocates
// ~440 transient bytes per indexed entry (measured: dirents, batch
// slices, append-growth abandonment) on top of the ~60 B/entry that
// stay live, and at the default GOGC=100 the pacer lets the heap run
// to ~2x live between cycles -- so a 10M-entry build peaks hundreds
// of MB above what it keeps. On macOS nothing else bounds that peak:
// the go-toolchain-injected memlimit guard derives GOMEMLIMIT from
// cgroups, which do not exist on darwin, and the startup summary's
// darwin RAM figure records the high-water mark. Lowering GOGC to 40
// for just the build window trades a few extra GC cycles inside an
// IO-bound walk for a materially lower peak.
//
// SetGCPercent is chosen over a derived GOMEMLIMIT deliberately: a
// percentage composes with any externally set GOMEMLIMIT (the pacer
// honors whichever bound is tighter), whereas computing our own byte
// limit could raise or fight the cgroup-derived one the linux guard
// installs.

// buildGCPercent is the temporary GOGC value in effect while the
// initial index build runs.
const buildGCPercent = 40

// boundBuildGC lowers GOGC to buildGCPercent through set (production:
// debug.SetGCPercent via the plat seam) and returns a restore func
// that puts the previous value back. A nil set (a test app that does
// not care) makes both steps no-ops.
func boundBuildGC(set func(int) int) (restore func()) {
	if set == nil {
		return func() {}
	}
	prev := set(buildGCPercent)
	return func() { set(prev) }
}
