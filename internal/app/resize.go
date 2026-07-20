package app

// Drag-edge window resizing: the frontend's edge zones (resize.ts)
// stream pointer-drag geometry through the bound ResizeDrag (one call
// per animation frame) and finish with ONE ResizeCommit on pointer
// release. Geometry semantics: horizontal drags resize ABOUT CENTER
// (the window's x shifts by half the width delta, so the bar stays
// horizontally centered on its display and the dragged edge tracks
// the pointer), vertical drags grow DOWNWARD from the anchored top.
// Every frame is clamped to the hosting display's usable area and the
// config floors (the clamp-to-screen rule, platform.ClampSize).
// Persistence happens ONLY at commit: the final size lands in
// config.json -- window.width/height, or preview.windowWidth/Height
// while the preview pane is mounted (the dragged size describes the
// CURRENT layout) -- through the self-write-suppressed atomic save,
// so the config-dir watcher never re-applies the app's own write. On
// Wayland the compositor owns placement, so drags resize without the
// centering shift (documented; the clamp still applies through the
// toolkit work-area probe).

import (
	"context"
	"crypto/sha256"
	"log"

	"github.com/wow-look-at-my/competent-search-thing/internal/config"
	"github.com/wow-look-at-my/competent-search-thing/internal/platform"
)

// maxDragDimension bounds frontend-echoed drag geometry (defense in
// depth, the RunPluginAction stance): nothing the math downstream
// touches ever sees a nonsensical magnitude. 32767 is the X11
// protocol's own dimension ceiling.
const maxDragDimension = 32767

// ResizeDrag applies one frame of an edge drag: the desired size is
// clamped and applied natively, and the window re-centers about its
// display's horizontal center with its top anchored. Cheap when
// nothing changed (the applied-size and placement dedups), safe to
// call at animation-frame rate. A no-op before Startup.
func (a *App) ResizeDrag(w, h int) {
	a.resizeTo(w, h)
}

// ResizeCommit applies the drag's final geometry, releases the drag
// anchor, and persists the result: ONE atomic config.json write of
// the dragged size (never per-frame). The error is the save failure,
// surfaced to the frontend's console; the live resize already
// happened either way.
func (a *App) ResizeCommit(w, h int) error {
	ew, eh := a.resizeTo(w, h)
	a.mu.Lock()
	a.dragActive = false
	a.dragDispOK = false
	a.dragPosOK = false
	a.mu.Unlock()
	if ew <= 0 || eh <= 0 {
		return nil // pre-Startup: nothing was applied, persist nothing
	}
	return a.persistDraggedSize(ew, eh)
}

// resizeTo clamps and applies one dragged geometry, returning the
// effective (applied) size; (0, 0) before Startup.
func (a *App) resizeTo(w, h int) (int, int) {
	ctx := a.runtimeCtx()
	if ctx == nil {
		return 0, 0
	}
	if w > maxDragDimension {
		w = maxDragDimension
	}
	if h > maxDragDimension {
		h = maxDragDimension
	}
	wayland := a.session().Kind == platform.SessionWayland
	disp, anchorY, dispOK, posOK := a.dragAnchor(ctx, wayland)

	// The clamp: the hosting display's usable area when known, the
	// toolkit work-area probe otherwise (Wayland's one source), the
	// config floors always.
	area, areaOK := platform.Rect{}, false
	if dispOK {
		area, areaOK = disp.UsableRect(), true
	} else if a.plat.windowWorkArea != nil {
		area, areaOK = a.plat.windowWorkArea()
	}
	w, h = platform.ClampSize(area, w, h, config.MinWindowWidth, config.MinWindowHeight)
	if posOK && areaOK {
		// Anchored top: growing downward stops at the usable area's
		// bottom edge (the floors still win on a pathological area).
		if maxH := area.Y + area.H - anchorY; maxH > 0 && h > maxH {
			h = maxH
			if h < config.MinWindowHeight {
				h = config.MinWindowHeight
			}
		}
	}

	// The dragged size IS the new desired size (the commit persists
	// it); positioning math follows it immediately.
	a.mu.Lock()
	a.winW, a.winH = w, h
	a.mu.Unlock()
	a.applySizeIfChanged(ctx, w, h)

	// Re-center about the display's horizontal center, top anchored.
	// Skipped on Wayland (compositor-owned placement) and when the
	// window's position is unknown (the rare centered-fallback show).
	if posOK && dispOK && !wayland {
		x := disp.Rect.X + (disp.Rect.W-w)/2
		ua := disp.UsableRect()
		if x+w > ua.X+ua.W {
			x = ua.X + ua.W - w
		}
		if x < ua.X {
			x = ua.X
		}
		a.moveTo(ctx, disp, x, anchorY)
	}
	return w, h
}

// dragAnchor returns the per-drag anchor, latching it on the first
// frame: the display hosting the window (the clamp area and the
// about-center math) and the window's current top y (the vertical
// anchor). The position comes from the app's own placement record
// (notePlacement -- the app is the only thing that moves this
// frameless window, and darwin cannot read it back), with the
// absolute WindowGetPosition read as the linux/windows fallback.
func (a *App) dragAnchor(ctx context.Context, wayland bool) (disp platform.Display, anchorY int, dispOK, posOK bool) {
	a.mu.Lock()
	if a.dragActive {
		disp, anchorY, dispOK, posOK = a.dragDisp, a.dragY, a.dragDispOK, a.dragPosOK
		a.mu.Unlock()
		return disp, anchorY, dispOK, posOK
	}
	px, py, placed := a.placedX, a.placedY, a.placedOK
	aw, ah := a.appliedW, a.appliedH
	a.mu.Unlock()

	if !wayland && a.plat.cursorInfo != nil {
		if cx, cy, displays, ok := a.plat.cursorInfo(); ok && len(displays) > 0 {
			wx, wy, havePos := px, py, placed
			if !havePos && a.plat.goos != "darwin" && a.rt.getPos != nil {
				wx, wy = a.rt.getPos(ctx)
				havePos = true
			}
			if havePos {
				if d, ok2 := platform.DisplayForWindow(displays, wx, wy, aw, ah); ok2 {
					disp, dispOK = d, true
					anchorY, posOK = wy, true
				}
			} else if d, ok2 := platform.PickDisplay(displays, cx, cy); ok2 {
				disp, dispOK = d, true
			}
		}
	}
	a.mu.Lock()
	a.dragActive = true
	a.dragDisp, a.dragY = disp, anchorY
	a.dragDispOK, a.dragPosOK = dispOK, posOK
	a.mu.Unlock()
	return disp, anchorY, dispOK, posOK
}

// moveTo places the window's top-left at the absolute coordinates
// (x, y) on disp -- the display the window is currently on -- using
// the same per-OS mechanisms the summon positioning uses, with a
// dedup against the last placement so unchanged frames cost nothing.
func (a *App) moveTo(ctx context.Context, disp platform.Display, x, y int) {
	a.mu.Lock()
	same := a.placedOK && a.placedX == x && a.placedY == y
	a.mu.Unlock()
	if same {
		return
	}
	if a.plat.goos == "darwin" {
		if a.plat.moveWindow(x, y) {
			a.notePlacement(x, y)
		}
		return
	}
	if a.rt.setPos == nil {
		return
	}
	rx, ry := platform.WailsPosition(a.plat.goos, disp, x, y)
	a.rt.setPos(ctx, rx, ry)
	a.notePlacement(x, y)
}

// persistDraggedSize writes the committed drag size to config.json:
// preview.windowWidth/Height while the preview pane is mounted (the
// dragged size describes the current widened layout), window.width/
// height otherwise. The write is the existing atomic save with the
// self-write checksum recorded first, so the config-dir watcher skips
// re-applying it, and the live-apply baseline is patched in place --
// the size is already live, nothing needs a second applier pass.
func (a *App) persistDraggedSize(w, h int) error {
	cfg, err := config.Load()
	if err != nil {
		// Never rewrite a file that could not be read back (a full
		// save would replace the user's config with repaired
		// defaults); the live resize stays, persistence waits.
		log.Printf("config: drag resize not persisted: %v", err)
		return err
	}
	previewMounted := a.previewConfig().Enabled
	if previewMounted {
		cfg.Preview.WindowWidth, cfg.Preview.WindowHeight = w, h
	} else {
		cfg.Window.Width, cfg.Window.Height = w, h
	}
	if data, eerr := config.Encode(cfg); eerr == nil {
		// Recorded BEFORE the save so the watcher's read of the
		// renamed file can never race an unset checksum.
		a.setLastSavedSum(sha256.Sum256(data))
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	a.cfgMu.Lock()
	if a.cfgCurrent != nil {
		cp := *a.cfgCurrent
		if previewMounted {
			cp.Preview.WindowWidth, cp.Preview.WindowHeight = w, h
		} else {
			cp.Window.Width, cp.Window.Height = w, h
		}
		a.cfgCurrent = &cp
	}
	a.cfgMu.Unlock()
	if previewMounted {
		log.Printf("config: drag resize saved preview.windowWidth/Height = %dx%d", w, h)
	} else {
		// The results column mirrors window.width while the pane is
		// off; keep the live value in step with what was persisted.
		a.mu.Lock()
		a.resultsW = w
		a.mu.Unlock()
		log.Printf("config: drag resize saved window.width/height = %dx%d", w, h)
	}
	return nil
}
