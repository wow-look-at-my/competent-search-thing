package ffext

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// maxTokenBytes bounds a parsed token: three int64s and two separators
// never legitimately approach it.
const maxTokenBytes = 64

// Token encodes one live tab's routing identity for the internal-only
// activate_tab action: "c<conn>:<tab>:<window>", all base-10. The
// frontend echoes it back verbatim; ParseToken re-validates strictly
// (the parseWindowID defense-in-depth stance).
func Token(connID, tabID, windowID int64) string {
	return fmt.Sprintf("c%d:%d:%d", connID, tabID, windowID)
}

// ParseToken decodes and validates an activate_tab token: a leading
// 'c', then exactly three base-10 non-negative int64 fields separated
// by ':' (the connection id additionally >= 1 -- ids start at 1).
func ParseToken(s string) (connID, tabID, windowID int64, err error) {
	if s == "" {
		return 0, 0, 0, errors.New("empty tab token")
	}
	if len(s) > maxTokenBytes {
		return 0, 0, 0, fmt.Errorf("tab token exceeds %d bytes", maxTokenBytes)
	}
	rest, ok := strings.CutPrefix(s, "c")
	if !ok {
		return 0, 0, 0, fmt.Errorf("tab token %q does not start with 'c'", s)
	}
	parts := strings.Split(rest, ":")
	if len(parts) != 3 {
		return 0, 0, 0, fmt.Errorf("tab token %q is not c<conn>:<tab>:<window>", s)
	}
	nums := make([]int64, 3)
	for i, p := range parts {
		n, perr := strconv.ParseInt(p, 10, 64)
		if perr != nil || n < 0 {
			return 0, 0, 0, fmt.Errorf("tab token %q field %d is not a non-negative base-10 integer", s, i)
		}
		nums[i] = n
	}
	if nums[0] < 1 {
		return 0, 0, 0, fmt.Errorf("tab token %q connection id must be >= 1", s)
	}
	return nums[0], nums[1], nums[2], nil
}
