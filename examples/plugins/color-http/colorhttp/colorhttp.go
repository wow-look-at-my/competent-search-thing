// Package colorhttp implements the HTTP side of the color-preview
// example plugin for competent-search-thing.
//
// It is written the way an EXTERNAL plugin author would write it:
// against the documented JSON wire format only, with its own request
// and response types -- it deliberately does not import the app's
// internal packages. The searchbar POSTs one JSON request per query
// to the URL in manifest.json and expects a 2xx JSON response;
// Handler ignores the request path, so any URL on the server works.
package colorhttp

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
)

// request is the slice of the documented request payload this plugin
// cares about: the query with the "#" trigger prefix already removed.
type request struct {
	Stripped string `json:"stripped"`
}

// response, result, field, and action mirror the documented response
// schema.
type response struct {
	V       int      `json:"v"`
	Results []result `json:"results"`
}

type result struct {
	Title       string  `json:"title"`
	Subtitle    string  `json:"subtitle,omitempty"`
	Icon        string  `json:"icon,omitempty"`
	Badge       string  `json:"badge,omitempty"`
	AccentColor string  `json:"accent_color,omitempty"`
	Score       int     `json:"score"`
	Fields      []field `json:"fields,omitempty"`
	Action      *action `json:"action,omitempty"`
}

type field struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

type action struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Handler serves the plugin protocol: POST one request, get one
// response. A query that parses as a hex color ("f80", "#336699")
// produces one swatch result; anything else produces an empty result
// list. Only POST is accepted (405 otherwise) and a malformed body is
// a 400 -- both surface in the searchbar log as plugin errors.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "plugin queries are POSTed", http.StatusMethodNotAllowed)
			return
		}
		var req request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "malformed request JSON", http.StatusBadRequest)
			return
		}
		resp := response{V: 1, Results: []result{}}
		if red, green, blue, ok := parseHexColor(req.Stripped); ok {
			resp.Results = append(resp.Results, swatch(red, green, blue))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
}

// swatch builds the one result for a parsed color.
func swatch(r, g, b int) result {
	canonical := fmt.Sprintf("#%02x%02x%02x", r, g, b)
	h, s, l := hsl(r, g, b)
	return result{
		Title:       canonical,
		Subtitle:    fmt.Sprintf("rgb(%d, %d, %d)", r, g, b),
		Icon:        "star",
		Badge:       "COLOR",
		AccentColor: canonical,
		Score:       100,
		Fields: []field{
			{Label: "R", Value: strconv.Itoa(r)},
			{Label: "G", Value: strconv.Itoa(g)},
			{Label: "B", Value: strconv.Itoa(b)},
			{Label: "H", Value: strconv.Itoa(h)},
			{Label: "S", Value: strconv.Itoa(s)},
			{Label: "L", Value: strconv.Itoa(l)},
		},
		Action: &action{Type: "copy_text", Value: canonical},
	}
}

// parseHexColor parses an optional leading '#' followed by exactly 3
// or 6 hex digits (case-insensitive) into 8-bit RGB channels. 3-digit
// shorthand doubles each digit ("f80" -> ff8800).
func parseHexColor(s string) (r, g, b int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	switch len(s) {
	case 3:
		expanded := make([]byte, 6)
		for i := 0; i < 3; i++ {
			expanded[2*i], expanded[2*i+1] = s[i], s[i]
		}
		s = string(expanded)
	case 6:
	default:
		return 0, 0, 0, false
	}
	var rgb [3]int
	for i := 0; i < 3; i++ {
		hi, okHi := hexNibble(s[2*i])
		lo, okLo := hexNibble(s[2*i+1])
		if !okHi || !okLo {
			return 0, 0, 0, false
		}
		rgb[i] = hi<<4 | lo
	}
	return rgb[0], rgb[1], rgb[2], true
}

// hexNibble decodes one hex digit.
func hexNibble(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

// hsl converts 8-bit RGB channels to integer HSL: hue in degrees
// (0..359), saturation and lightness in percent (0..100).
func hsl(r, g, b int) (h, s, l int) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(rf, math.Max(gf, bf))
	min := math.Min(rf, math.Min(gf, bf))
	lf := (max + min) / 2
	if max == min {
		return 0, 0, int(math.Round(lf * 100))
	}
	d := max - min
	sf := d / (1 - math.Abs(2*lf-1))
	var hf float64
	switch max {
	case rf:
		hf = math.Mod((gf-bf)/d, 6)
	case gf:
		hf = (bf-rf)/d + 2
	default:
		hf = (rf-gf)/d + 4
	}
	hf *= 60
	if hf < 0 {
		hf += 360
	}
	return int(math.Round(hf)) % 360, int(math.Round(sf * 100)), int(math.Round(lf * 100))
}
