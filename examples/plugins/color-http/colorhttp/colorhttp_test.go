package colorhttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// post drives Handler with one plugin request whose stripped query is
// the given string, and returns the recorded response.
func post(t *testing.T, stripped string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(request{Stripped: stripped})
	require.NoError(t, err)
	return raw(t, http.MethodPost, "/query", string(body))
}

// raw drives Handler with an arbitrary method, path, and body.
func raw(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	return rec
}

// decode parses a 200 response body.
func decode(t *testing.T, rec *httptest.ResponseRecorder) response {
	t.Helper()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var resp response
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp
}

func TestHandlerSixDigitColor(t *testing.T) {
	resp := decode(t, post(t, "ff0000"))
	require.Equal(t, 1, resp.V)
	require.Equal(t, []result{{
		Title:       "#ff0000",
		Subtitle:    "rgb(255, 0, 0)",
		Icon:        "star",
		Badge:       "COLOR",
		AccentColor: "#ff0000",
		Score:       100,
		Fields: []field{
			{Label: "R", Value: "255"},
			{Label: "G", Value: "0"},
			{Label: "B", Value: "0"},
			{Label: "H", Value: "0"},
			{Label: "S", Value: "100"},
			{Label: "L", Value: "50"},
		},
		Action: &action{Type: "copy_text", Value: "#ff0000"},
	}}, resp.Results)
}

func TestHandlerThreeDigitShorthand(t *testing.T) {
	resp := decode(t, post(t, "f80"))
	require.Len(t, resp.Results, 1)
	r := resp.Results[0]
	require.Equal(t, "#ff8800", r.Title, "3-digit shorthand doubles each digit")
	require.Equal(t, "rgb(255, 136, 0)", r.Subtitle)
	require.Equal(t, "#ff8800", r.AccentColor)
	require.Equal(t, []field{
		{Label: "R", Value: "255"},
		{Label: "G", Value: "136"},
		{Label: "B", Value: "0"},
		{Label: "H", Value: "32"},
		{Label: "S", Value: "100"},
		{Label: "L", Value: "50"},
	}, r.Fields)
	require.Equal(t, &action{Type: "copy_text", Value: "#ff8800"}, r.Action)
}

func TestHandlerHashPrefixAndCase(t *testing.T) {
	resp := decode(t, post(t, "#808080"))
	require.Len(t, resp.Results, 1)
	r := resp.Results[0]
	require.Equal(t, "#808080", r.Title)
	require.Equal(t, []field{
		{Label: "R", Value: "128"},
		{Label: "G", Value: "128"},
		{Label: "B", Value: "128"},
		{Label: "H", Value: "0"},
		{Label: "S", Value: "0"},
		{Label: "L", Value: "50"},
	}, r.Fields, "achromatic gray: H 0, S 0, L rounds to 50")

	upper := decode(t, post(t, "#FF8800"))
	require.Len(t, upper.Results, 1)
	require.Equal(t, "#ff8800", upper.Results[0].Title, "canonical form is lowercase")
}

func TestHandlerPathAgnostic(t *testing.T) {
	body, err := json.Marshal(request{Stripped: "#123456"})
	require.NoError(t, err)
	rec := raw(t, http.MethodPost, "/some/other/path", string(body))
	resp := decode(t, rec)
	require.Len(t, resp.Results, 1)
	require.Equal(t, "#123456", resp.Results[0].Title)
}

func TestHandlerNonColorReturnsEmptyResults(t *testing.T) {
	for _, stripped := range []string{
		"", "z", "zz", "#zz", "12345", "1234567", "#ff80", "not a color", "ggg", "#12g456",
	} {
		t.Run(fmt.Sprintf("%q", stripped), func(t *testing.T) {
			rec := post(t, stripped)
			resp := decode(t, rec)
			require.Equal(t, 1, resp.V)
			require.Empty(t, resp.Results)
			require.Contains(t, rec.Body.String(), `"results":[]`,
				"empty results are an explicit [] on the wire, not null")
		})
	}
}

func TestHandlerRejectsNonPOST(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := raw(t, method, "/query", "")
		require.Equal(t, http.StatusMethodNotAllowed, rec.Code, method)
	}
}

func TestHandlerRejectsMalformedBody(t *testing.T) {
	rec := raw(t, http.MethodPost, "/query", "{not json")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHSLKnownColors(t *testing.T) {
	tests := []struct {
		name    string
		r, g, b int
		h, s, l int
	}{
		{name: "red", r: 255, g: 0, b: 0, h: 0, s: 100, l: 50},
		{name: "lime", r: 0, g: 255, b: 0, h: 120, s: 100, l: 50},
		{name: "blue", r: 0, g: 0, b: 255, h: 240, s: 100, l: 50},
		{name: "magenta wraps negative hue", r: 255, g: 0, b: 255, h: 300, s: 100, l: 50},
		{name: "yellow", r: 255, g: 255, b: 0, h: 60, s: 100, l: 50},
		{name: "orange", r: 255, g: 136, b: 0, h: 32, s: 100, l: 50},
		{name: "white", r: 255, g: 255, b: 255, h: 0, s: 0, l: 100},
		{name: "black", r: 0, g: 0, b: 0, h: 0, s: 0, l: 0},
		{name: "gray", r: 128, g: 128, b: 128, h: 0, s: 0, l: 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, s, l := hsl(tt.r, tt.g, tt.b)
			require.Equal(t, [3]int{tt.h, tt.s, tt.l}, [3]int{h, s, l})
		})
	}
}

func TestParseHexColor(t *testing.T) {
	tests := []struct {
		in      string
		r, g, b int
		ok      bool
	}{
		{in: "ff8800", r: 255, g: 136, b: 0, ok: true},
		{in: "#ff8800", r: 255, g: 136, b: 0, ok: true},
		{in: "f80", r: 255, g: 136, b: 0, ok: true},
		{in: "#AbC", r: 0xaa, g: 0xbb, b: 0xcc, ok: true},
		{in: "  #123456  ", r: 0x12, g: 0x34, b: 0x56, ok: true},
		{in: ""},
		{in: "#"},
		{in: "ff"},
		{in: "ffff"},
		{in: "fffffff"},
		{in: "xyz"},
		{in: "12345z"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q", tt.in), func(t *testing.T) {
			r, g, b, ok := parseHexColor(tt.in)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				require.Equal(t, [3]int{tt.r, tt.g, tt.b}, [3]int{r, g, b})
			}
		})
	}
}
