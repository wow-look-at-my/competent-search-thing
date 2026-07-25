package index

import (
	"math/rand"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// refEntryAt is the binary search entryAt replaced: the reference the
// probe-then-search version must agree with on every input.
func refEntryAt(s *Store, cur, hi int, pos uint32) int {
	return cur + sort.Search(hi-cur, func(k int) bool { return s.nameOff[cur+k+1] > pos })
}

// TestEntryAtMatchesBinarySearch sweeps every legal (cur, pos) pair over
// stores whose name lengths vary, including runs far longer than
// entryProbeSteps so both the probe and the binary-search tail are
// exercised.
func TestEntryAtMatchesBinarySearch(t *testing.T) {
	rng := rand.New(rand.NewSource(20260725))
	for _, n := range []int{1, 2, 7, entryProbeSteps, entryProbeSteps + 1, 64, 500} {
		st := NewStore()
		for i := 0; i < n; i++ {
			// Names of wildly different lengths so the offset table is
			// irregular, not a fixed stride the search could luck into.
			name := make([]byte, 1+rng.Intn(24))
			for j := range name {
				name[j] = byte('a' + rng.Intn(26))
			}
			_, err := st.AddEntry("/r", string(name)+itoa(i), false)
			require.NoError(t, err)
		}
		hi := st.Len()
		require.Equal(t, n, hi)
		for cur := 0; cur < hi; cur++ {
			// Every blob position owned by an entry at or after cur.
			for pos := st.nameOff[cur]; pos < st.nameOff[hi]; pos++ {
				want := refEntryAt(st, cur, hi, pos)
				got := st.entryAt(cur, hi, pos)
				require.Equal(t, want, got,
					"n=%d cur=%d pos=%d", n, cur, pos)
			}
		}
	}
}

// TestEntryAtOwnsPosition states the contract directly, independent of
// the reference: the returned entry is the one whose name spans pos.
func TestEntryAtOwnsPosition(t *testing.T) {
	st := NewStore()
	for i := 0; i < 200; i++ {
		_, err := st.AddEntry("/r", "n"+itoa(i), false)
		require.NoError(t, err)
	}
	hi := st.Len()
	for cur := 0; cur < hi; cur += 7 {
		for pos := st.nameOff[cur]; pos < st.nameOff[hi]; pos += 3 {
			e := st.entryAt(cur, hi, pos)
			require.GreaterOrEqual(t, e, cur, "never walks backwards")
			require.Less(t, e, hi, "stays inside the shard")
			require.LessOrEqual(t, st.nameOff[e], pos, "name starts at or before pos")
			require.Greater(t, st.nameOff[e+1], pos, "name ends after pos")
		}
	}
}

// itoa keeps the fixtures dependency-free (strconv would do, but this
// makes the generated names obvious at a glance).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
