package index

// Numeric-aware final tie-break: the LAST comparator step, replacing
// the plain lexicographic full-path comparison ONLY there. Two
// candidates that tie through the whole existing chain (class,
// alignment, dirs-first, path length) and whose paths carry digit
// runs at aligned positions compare those runs NUMERICALLY
// DESCENDING, so datestamped and versioned families deliver newest
// first ("Screenshot 2026-07-18..." above "Screenshot 2024-02-01...",
// "invoice_v2" above "invoice_v1") instead of the plain byte order's
// oldest-first. Everything earlier in the chain is untouched, and a
// first difference that is not digits-vs-digits keeps the plain byte
// order, so orderings between unrelated names never move.
//
// The comparison is the classic version-sort walk (strverscmp-style,
// numbers inverted), which IS a total order: tokens are maximal digit
// runs or single non-digit bytes, walked in lockstep; digit runs
// order among themselves by numeric value descending, non-digit bytes
// by byte value, and a run against a non-digit byte by that byte
// against the run's first digit -- consistent because the digits
// 0x30..0x39 are contiguous in ASCII, so every non-digit byte sits
// entirely below or entirely above every digit run. Numerically equal
// runs with different spellings ("07" vs "7") count as equal and the
// walk continues; a walk that ends all-equal falls back to the plain
// lexicographic comparison so the order stays total (paths are
// unique).
//
// Deliberately NOT applied anywhere earlier: shorter-path still beats
// longer ("v9" still precedes "v10" -- their path lengths differ, so
// the chain decides before this step), and internal/match's Rank
// (plugin-row ordering) is untouched.

// isDigitByte reports whether b is an ASCII digit.
func isDigitByte(b byte) bool { return '0' <= b && b <= '9' }

// compareJoinedNumeric compares the virtual full paths
// joinDir(da, na) and joinDir(db, nb) with the numeric-aware walk
// described above, without allocating them. Negative means the first
// path ranks first.
func compareJoinedNumeric(da string, na []byte, db string, nb []byte) int {
	la := joinedLen(da, len(na))
	lb := joinedLen(db, len(nb))
	i, j := 0, 0
	for i < la && j < lb {
		ca := joinedAt(da, na, i)
		cb := joinedAt(db, nb, j)
		if isDigitByte(ca) && isDigitByte(cb) {
			// Two aligned digit runs: consume both fully and compare
			// numerically. Descending -- the larger number ranks
			// first. Equal values (leading-zero spellings included)
			// continue the walk.
			ia, ja := i, j
			for i < la && isDigitByte(joinedAt(da, na, i)) {
				i++
			}
			for j < lb && isDigitByte(joinedAt(db, nb, j)) {
				j++
			}
			if c := compareRuns(da, na, ia, i, db, nb, ja, j); c != 0 {
				return -c // numeric DESCENDING: bigger ranks first
			}
			continue
		}
		if ca != cb {
			// Any other first difference -- non-digit vs non-digit,
			// or digit vs non-digit -- keeps the plain byte order.
			if ca < cb {
				return -1
			}
			return 1
		}
		i++
		j++
	}
	if i < la {
		return 1 // b is a proper prefix: shorter first, as before
	}
	if j < lb {
		return -1
	}
	// All tokens equivalent. Distinct paths can still land here via
	// leading-zero spellings ("img07" vs "img7" cannot -- lengths
	// differ -- but "a01b2" vs "a1b02" can): fall back to the plain
	// lexicographic order so the comparator stays a total order.
	return compareJoined(da, na, db, nb)
}

// compareRuns numerically compares the digit run at bytes [ia, ea) of
// joinDir(da, na) against the run at [jb, eb) of joinDir(db, nb),
// ASCENDING, without materializing either: leading zeros are skipped,
// a longer remaining run is the bigger number, and equal-length
// remainders compare byte-wise.
func compareRuns(da string, na []byte, ia, ea int, db string, nb []byte, jb, eb int) int {
	for ia < ea && joinedAt(da, na, ia) == '0' {
		ia++
	}
	for jb < eb && joinedAt(db, nb, jb) == '0' {
		jb++
	}
	if wa, wb := ea-ia, eb-jb; wa != wb {
		if wa < wb {
			return -1
		}
		return 1
	}
	for ia < ea {
		ca := joinedAt(da, na, ia)
		cb := joinedAt(db, nb, jb)
		if ca != cb {
			if ca < cb {
				return -1
			}
			return 1
		}
		ia++
		jb++
	}
	return 0
}
