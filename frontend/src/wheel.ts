// shouldInterceptWheel decides whether main.ts installs the manual
// wheel-to-scrollTop interception on #results. The interception
// exists for WebKitGTK: its default-on smooth-scroll animator eats
// fast detents and Wails exposes no knob, so Linux applies deltas
// straight to scrollTop (see the listener in main.ts). On macOS the
// same non-passive always-preventDefault listener is actively
// HARMFUL: it puts #results in WebKit's NonPassiveWheel region, which
// routes every wheel event through the SYNCHRONOUS main-thread scroll
// path and repaints motion at the rendering-update cadence -- 30fps
// under the macOS Low Power Mode throttle. With no listener at all,
// the overflow-scrolled list rides the async scrolling thread at
// display rate with native trackpad momentum, and ctrl+wheel zoom
// stays native exactly as before. navigator.platform reports
// "MacIntel" on ALL Macs (Apple Silicon included) -- prefix-match
// "Mac". Pure and vitest-covered; Linux/Windows behavior stays
// byte-identical (the handler registers exactly as it always did).
export function shouldInterceptWheel(platform: string): boolean {
  return !platform.startsWith("Mac");
}
