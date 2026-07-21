package icons

import (
	"bytes"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Website favicon resolution ("favicon:<pageURL>" keys, stamped by the
// builtin Firefox result sources). Three tiers, in order:
//
//  1. The browser hint: NoteFavicon records the favicon URL Firefox
//     itself reports per page (the companion extension's live
//     favIconUrl, or the sessionstore snapshot's image attribute). A
//     data: hint decodes and serves directly -- zero IO; an http(s)
//     hint becomes a fetch candidate for tier 3.
//  2. Offline: the Options.FaviconLookup seam (production =
//     internal/firefox's FaviconReader over the profile's
//     favicons.sqlite snapshot) answers stored bytes, or a known
//     favicon URL when it has none.
//  3. Last resort, bounded: ONE http(s) GET of a KNOWN favicon URL
//     (the tier-1 hint or the tier-2 icon URL -- never a guessed
//     /favicon.ico) under hard caps: 3s total, 256 KiB body, 3
//     redirect hops (http(s)-only), single-flight per key. Success
//     lands in the positive LRU; any miss negative-caches into the
//     glyph -- which stays the honest fallback for icon-less rows.
//
// Every payload from every tier is SNIFFED (imageMIME) before it can
// reach the frontend: declared MIME types and file extensions are
// never trusted, junk misses into the glyph. The whole path runs
// OUTSIDE the service mutex (see Resolve's second phase), so a slow
// fetch can never stall app-icon or file-icon resolution.

// Favicon bounds.
const (
	faviconFetchTimeout  = 3 * time.Second
	faviconFetchMaxBytes = 256 << 10
	faviconMaxRedirects  = 3
	// faviconMaxHintBytes caps one stored NoteFavicon hint value
	// (data: URIs are the big case; real favicon data URIs are a few
	// KB, the cap only bounds hostile input).
	faviconMaxHintBytes = 64 << 10
	// faviconMaxPageBytes caps the page-URL half of keys and hints.
	faviconMaxPageBytes = 2048
)

// NoteFavicon records the browser-reported favicon location for an
// http(s) page: an http(s) URL or a data:image/* URI (anything else
// -- including Firefox-internal schemes like fake-favicon-uri: -- is
// dropped). The hints ride a bounded LRU beside the icon caches; a
// CHANGED hint for a page also un-pins that page's negative-cached
// misses, so a favicon that only became known after a miss (the
// extension connected later) resolves on the next request instead of
// staying a glyph until eviction. Goroutine-safe, never blocks on IO.
func (s *Service) NoteFavicon(pageURL, favURL string) {
	if len(pageURL) > faviconMaxPageBytes || !validPageURL(pageURL) {
		return
	}
	if len(favURL) > faviconMaxHintBytes {
		return
	}
	if !strings.HasPrefix(favURL, "data:image/") && !validPageURL(favURL) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.favHints.get(pageURL); ok && prev == favURL {
		return
	}
	s.favHints.put(pageURL, favURL)
	s.negative.deletePrefix(keyFaviconPrefix + pageURL + "|")
}

// resolveFavicon serves one favicon key through the two-level cache
// plus a per-key single-flight gate: concurrent requests for the same
// key share ONE resolution (and one network fetch at most), later
// requests re-read the caches the winner filled. Runs WITHOUT the
// service mutex held across the resolution work.
func (s *Service) resolveFavicon(key string, size int) (string, bool) {
	page := strings.TrimPrefix(key, keyFaviconPrefix)
	if len(page) > faviconMaxPageBytes || !validPageURL(page) {
		return "", false
	}
	ck := key + "|" + strconv.Itoa(size)
	for {
		s.mu.Lock()
		if uri, ok := s.cache.get(ck); ok {
			s.mu.Unlock()
			return uri, true
		}
		if _, neg := s.negative.get(ck); neg {
			s.mu.Unlock()
			return "", false
		}
		if ch, busy := s.favInflight[ck]; busy {
			s.mu.Unlock()
			<-ch // another goroutine is resolving this key; wait, re-read
			continue
		}
		ch := make(chan struct{})
		s.favInflight[ck] = ch
		s.mu.Unlock()

		uri := s.buildFaviconURI(page, size)

		s.mu.Lock()
		if uri != "" {
			s.cache.put(ck, uri)
		} else {
			s.negative.put(ck, "")
		}
		delete(s.favInflight, ck)
		s.mu.Unlock()
		close(ch)
		return uri, uri != ""
	}
}

// buildFaviconURI runs the uncached three-tier resolution for one
// page; "" on a total miss.
func (s *Service) buildFaviconURI(page string, size int) string {
	s.mu.Lock()
	hint, _ := s.favHints.get(page)
	s.mu.Unlock()

	// Tier 1: a data: hint serves directly (no IO at all).
	if strings.HasPrefix(hint, "data:") {
		if uri := s.dataURIFromHint(hint); uri != "" {
			return uri
		}
		hint = "" // undecodable data hint: nothing left to fetch from it
	}

	// Tier 2: the offline favicons.sqlite snapshot.
	var knownURL string
	if s.faviconLookup != nil {
		data, iconURL := s.faviconLookup(page, size)
		if uri := s.imageDataURI(data); uri != "" {
			return uri
		}
		knownURL = iconURL
	}

	// Tier 3: one bounded fetch of a KNOWN favicon URL.
	for _, cand := range [...]string{hint, knownURL} {
		if cand == "" || !validPageURL(cand) {
			continue
		}
		if uri := s.imageDataURI(s.fetchFavicon(cand)); uri != "" {
			return uri
		}
	}
	return ""
}

// dataURIFromHint decodes a browser-supplied data: URI hint and
// re-mints it from the DECODED bytes: only base64 payloads are
// accepted, the sniffed MIME wins over the declared one, and the
// byte caps apply -- a hint is browser data, not trusted input.
func (s *Service) dataURIFromHint(hint string) string {
	rest, ok := strings.CutPrefix(hint, "data:")
	if !ok {
		return ""
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return ""
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return ""
	}
	return s.imageDataURI(data)
}

// imageDataURI validates raw image bytes (sniffed type, size caps)
// and encodes them as a data URI; "" when the bytes are absent,
// oversized, or not a renderable image.
func (s *Service) imageDataURI(data []byte) string {
	if len(data) == 0 || int64(len(data)) > s.maxFileBytes {
		return ""
	}
	mime := imageMIME(data)
	if mime == "" {
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// fetchFavicon performs the tier-3 bounded GET; nil on any failure
// (the caller misses into the glyph). The client enforces the total
// timeout and the http(s)-only redirect cap; the body read enforces
// the size cap.
func (s *Service) fetchFavicon(rawURL string) []byte {
	resp, err := s.favClient.Get(rawURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, s.favMaxFetch+1))
	if err != nil || int64(len(data)) > s.favMaxFetch {
		return nil
	}
	return data
}

// newFaviconClient builds the tier-3 HTTP client: total-exchange
// timeout, capped http(s)-only redirects. rt/timeout are the test
// seams (zero = production values).
func newFaviconClient(rt http.RoundTripper, timeout time.Duration) *http.Client {
	if rt == nil {
		rt = http.DefaultTransport
	}
	if timeout <= 0 {
		timeout = faviconFetchTimeout
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: rt,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > faviconMaxRedirects {
				return errors.New("favicon: too many redirects")
			}
			if !validPageURL(req.URL.String()) {
				return errors.New("favicon: non-http(s) redirect")
			}
			return nil
		},
	}
}

// validPageURL reports whether raw is an absolute http(s) URL with a
// host -- the shape favicon keys, hints, and fetch candidates must
// all have.
func validPageURL(raw string) bool {
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	return (scheme == "http" || scheme == "https") && u.Host != ""
}

// imageMIME sniffs data's image type by magic bytes (SVG by content;
// pngMagic lives in icns.go); "" for anything the frontend could not
// render as an <img>. Firefox stores favicons as PNG, ICO, or SVG;
// the network can hand back anything, hence the wider table.
func imageMIME(data []byte) string {
	switch {
	case bytes.HasPrefix(data, pngMagic):
		return "image/png"
	case bytes.HasPrefix(data, []byte("GIF87a")) || bytes.HasPrefix(data, []byte("GIF89a")):
		return "image/gif"
	case bytes.HasPrefix(data, []byte{0xFF, 0xD8, 0xFF}):
		return "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return "image/webp"
	case bytes.HasPrefix(data, []byte{0x00, 0x00, 0x01, 0x00}):
		return "image/x-icon"
	case bytes.HasPrefix(data, []byte("BM")):
		return "image/bmp"
	case sniffSVG(data):
		return "image/svg+xml"
	}
	return ""
}

// sniffSVG reports whether data looks like an SVG document: markup
// (after optional BOM and whitespace) whose first KB contains an
// <svg element.
func sniffSVG(data []byte) bool {
	head := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	head = bytes.TrimLeft(head, " \t\r\n")
	if !bytes.HasPrefix(head, []byte("<")) {
		return false
	}
	if len(head) > 1024 {
		head = head[:1024]
	}
	return bytes.Contains(head, []byte("<svg"))
}
