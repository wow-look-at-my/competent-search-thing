// Theme plumbing: fetches the resolved design tokens (GetTheme) and
// the optional custom.css escape hatch (GetCustomCSS) from the Go side
// and applies them to the document. Each token k becomes the CSS
// custom property --sb-k on <html>, overriding the dark fallback
// values that style.css declares in :root; the custom stylesheet is
// injected as the text of one managed <style> element. The Go side
// emits "theme:changed" whenever config.json or anything under the
// themes/ directory changes, and everything is re-fetched and
// re-applied -- theme edits show up live.

import { isLightBackground } from "./fileicons/fileicons";

const TOKEN_PREFIX = "--sb-";
const CUSTOM_STYLE_ID = "sb-custom-css";

// Tokens applied by the previous fetch, so properties that disappear
// from the map (defensive; the Go side always sends the full set) are
// removed rather than left stale.
let appliedTokens = new Set<string>();

function applyTokens(tokens: Record<string, string>): void {
  const style = document.documentElement.style;
  const next = new Set<string>();
  for (const [name, value] of Object.entries(tokens)) {
    style.setProperty(TOKEN_PREFIX + name, value);
    next.add(name);
  }
  for (const name of appliedTokens) {
    if (!next.has(name)) {
      style.removeProperty(TOKEN_PREFIX + name);
    }
  }
  appliedTokens = next;
  // Select the file-icon colour variant from the background this
  // theme carries (the file-icons pack's own light/dark motif rule);
  // fileicons.css keys every glyph colour on this class, so a live
  // theme switch recolors already-rendered rows with zero re-renders.
  // Re-runs on every "theme:changed" refetch via fetchAndApply.
  document.documentElement.classList.toggle(
    "icons-light",
    isLightBackground(tokens["bg"] ?? ""),
  );
}

function applyCustomCSS(css: string): void {
  let el = document.getElementById(CUSTOM_STYLE_ID);
  if (!(el instanceof HTMLStyleElement)) {
    el?.remove();
    el = document.createElement("style");
    el.id = CUSTOM_STYLE_ID;
    document.head.appendChild(el);
  }
  el.textContent = css;
}

async function fetchAndApply(app: WailsAppBindings): Promise<void> {
  const [tokens, customCSS] = await Promise.all([
    app.GetTheme(),
    app.GetCustomCSS(),
  ]);
  applyTokens(tokens);
  applyCustomCSS(customCSS);
}

// initTheme applies the configured theme immediately and re-applies it
// on every "theme:changed" runtime event. Failures are logged and
// leave the current (or the CSS-fallback dark) look in place.
export function initTheme(app: WailsAppBindings, rt: WailsRuntime): void {
  const refresh = (): void => {
    fetchAndApply(app).catch((err: unknown) => {
      console.error("theme: applying failed:", err);
    });
  };
  rt.EventsOn("theme:changed", refresh);
  refresh();
}
