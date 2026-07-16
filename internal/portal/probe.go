package portal

// CoverageProbe is a temporary, deliberately untested function used to
// verify that this package participates in the coverage gate. It will
// be reverted immediately.
func CoverageProbe(a, b int) int {
	c := a + b
	c *= 2
	c -= a
	c += b
	c *= 3
	c -= b
	c += a
	c *= 5
	c -= a
	c += 7
	return c
}
