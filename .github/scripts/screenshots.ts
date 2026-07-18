// CI screenshot capture: boots the real app under Xvfb, summons it with the
// real Alt+Space X11 grab, types a query, and captures three PNGs PER THEME
// (dark, light) into screenshots/<theme>/ at the workspace root. Each theme
// gets a fresh app process reading a config.json with that theme set; Xvfb
// and openbox stay up across themes. Any failure (window never maps, blank
// webview, hotkey grab refused, Escape does not hide) fails the step -- a
// missing or blank bar is a real UI regression signal.
//
// Runs via wow-look-at-my/actions@typescript#latest (file: input). Injected
// globals used: core, $, fs, path, os, child_process, env.
//
// The equivalent local capture flow is documented in CLAUDE.md.

const workspace = env.GITHUB_WORKSPACE ?? process.cwd();
process.env.DISPLAY = ":99"; // inherited by $ commands and spawned children

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

async function pollFor<T>(
  what: string,
  deadlineMs: number,
  intervalMs: number,
  probe: () => Promise<T | undefined>,
): Promise<T> {
  const deadline = Date.now() + deadlineMs;
  for (;;) {
    const got = await probe();
    if (got !== undefined) return got;
    if (Date.now() > deadline) throw new Error(`timed out after ${deadlineMs}ms waiting for ${what}`);
    await sleep(intervalMs);
  }
}

// ---- locate the built binary ------------------------------------------------
// go-toolchain matrix (the CI action) names artifacts <name>_<os>_<arch>;
// a plain local go-toolchain run names it <name>. Try both.
const candidates = [
  path.join(workspace, "build", "competent-search-thing_linux_amd64"),
  path.join(workspace, "build", "competent-search-thing"),
];
const builtBin = candidates.find((p) => fs.existsSync(p));
if (builtBin === undefined) {
  const buildDir = path.join(workspace, "build");
  const listing = fs.existsSync(buildDir) ? fs.readdirSync(buildDir).join(", ") : "(no build/ directory)";
  core.setFailed(
    `screenshots: built binary not found (tried ${candidates.join(", ")}); build/ contains: ${listing}. ` +
      "The go-toolchain step must run first and GOFLAGS must include the desktop,production tags.",
  );
  return;
}

// Never execute build/ artifacts in place (go-toolchain README: matrix
// artifacts must stay pristine for upload/publishing) -- run a throwaway copy.
const work = fs.mkdtempSync(path.join(os.tmpdir(), "searchbar-shots-"));
const bin = path.join(work, "competent-search-thing");
fs.copyFileSync(builtBin, bin);
fs.chmodSync(bin, 0o755);

// ---- deterministic fixture tree + config ------------------------------------
// Short root so parent-dir labels in the shots stay readable.
const fixture = path.join(os.tmpdir(), "demo");
fs.rmSync(fixture, { recursive: true, force: true });
const dirs = [
  "Documents/Reports",
  "Documents/Invoices",
  "Documents/Taxes-2025",
  "Projects/searchbar",
  "Projects/webapp/src",
  "Projects/webapp/public",
  "Pictures/Vacation-2025",
  "Pictures/Screenshots",
  "Music/Playlists",
  "Videos",
  "Downloads",
  ".git/objects", // excluded -- proves exclude patterns work
  "node_modules/lodash", // excluded
];
for (const d of dirs) fs.mkdirSync(path.join(fixture, d), { recursive: true });
const files = [
  "Documents/quarterly-report-2026.pdf",
  "Documents/quarterly-report-2025.pdf",
  "Documents/annual-report-2025-final.pdf",
  "Documents/expense-report-march.xlsx",
  "Documents/resume.docx",
  "Documents/cover-letter.docx",
  "Documents/meeting-notes.md",
  "Documents/Reports/report-draft.md",
  "Documents/Reports/sales-report-q1.csv",
  "Documents/Reports/sales-report-q2.csv",
  "Documents/Invoices/invoice-0042.pdf",
  "Documents/Invoices/invoice-0043.pdf",
  "Documents/Taxes-2025/w2-form.pdf",
  "Documents/Taxes-2025/deductions.xlsx",
  "Projects/searchbar/main.go",
  "Projects/searchbar/README.md",
  "Projects/searchbar/go.mod",
  "Projects/searchbar/index.go",
  "Projects/webapp/package.json",
  "Projects/webapp/README.md",
  "Projects/webapp/src/app.ts",
  "Projects/webapp/src/style.css",
  "Projects/webapp/src/index.html",
  "Projects/webapp/public/favicon.ico",
  "Projects/webapp/public/logo.svg",
  "Pictures/Vacation-2025/beach-sunset.jpg",
  "Pictures/Vacation-2025/mountain-trail.jpg",
  "Pictures/Screenshots/screenshot-2026-07-01.png",
  "Pictures/Screenshots/screenshot-2026-07-08.png",
  "Music/road-trip-mix.mp3",
  "Music/acoustic-covers.mp3",
  "Music/Playlists/favorites.m3u",
  "Videos/birthday-party-2025.mp4",
  "Videos/screen-recording-demo.mkv",
  "Downloads/ubuntu-24.04-desktop-amd64.iso",
  "Downloads/presentation-slides.pptx",
  "Downloads/dataset-export.csv",
  "README.md",
  "notes.txt",
  "todo.md",
  ".git/config",
  ".git/HEAD",
  "node_modules/lodash/index.js",
  "node_modules/lodash/package.json",
];
for (const i of ["2031", "2032", "2033", "2034", "2035", "2040", "2041"]) files.push(`Pictures/IMG_${i}.jpg`);
for (let i = 1; i <= 60; i++) files.push(`Downloads/archive-item-${String(i).padStart(2, "0")}.dat`);
for (let i = 1; i <= 40; i++) files.push(`Music/track-${String(i).padStart(2, "0")}.mp3`);
for (let i = 1; i <= 40; i++) files.push(`Pictures/photo-batch2-${String(i).padStart(2, "0")}.jpg`);
for (const f of files) fs.writeFileSync(path.join(fixture, f), "");

const cfgDir = path.join(work, "cfg");
fs.mkdirSync(cfgDir, { recursive: true });
function writeConfig(theme: string): void {
  fs.writeFileSync(
    path.join(cfgDir, "config.json"),
    JSON.stringify(
      {
        roots: [fixture],
        excludes: [".git", "node_modules", ".cache"],
        hotkey: "alt+space",
        rescanIntervalMinutes: 0,
        maxResults: 50,
        theme,
      },
      null,
      2,
    ),
  );
}

// ---- openbox config: strip the default A-space binding ----------------------
// Stock openbox binds Alt+Space to the client menu; that grab wins the race
// and the app's XGrabKey gets BadAccess. Remove the binding.
const rcSrc = "/etc/xdg/openbox/rc.xml";
if (!fs.existsSync(rcSrc)) {
  core.setFailed(`screenshots: ${rcSrc} not found -- is openbox installed? (apt-get install openbox)`);
  return;
}
const rcPath = path.join(work, "openbox-rc.xml");
fs.writeFileSync(rcPath, fs.readFileSync(rcSrc, "utf-8").replace(/<keybind key="A-space">[\s\S]*?<\/keybind>/g, ""));

// ---- process management ------------------------------------------------------
interface Proc {
  name: string;
  child: import("child_process").ChildProcess;
  log: string;
}
const procs: Proc[] = [];

function spawnLogged(name: string, cmd: string, args: string[], extraEnv: Record<string, string>): Proc {
  const child = child_process.spawn(cmd, args, {
    env: { ...process.env, ...extraEnv },
    stdio: ["ignore", "pipe", "pipe"],
  });
  const p: Proc = { name, child, log: "" };
  child.stdout?.on("data", (d: Buffer) => (p.log += String(d)));
  child.stderr?.on("data", (d: Buffer) => (p.log += String(d)));
  procs.push(p);
  return p;
}

async function stop(p: Proc): Promise<void> {
  if (p.child.exitCode !== null || p.child.killed) return;
  p.child.kill("SIGTERM");
  const gone = await Promise.race([
    new Promise<boolean>((r) => p.child.once("exit", () => r(true))),
    sleep(3000).then(() => false),
  ]);
  if (!gone) p.child.kill("SIGKILL");
}

// Per-theme assertion bounds, derived from real local captures on the same
// fixture (do not guess -- re-derive if the UI changes). Measured values
// (re-derived 2026-07-18, when the window grew to 780x550 and gained the
// bottom stats row; the bands still hold with wide margins, so only these
// evidence numbers changed):
//   dark  01/02/03 = 26576/68782/68810 bytes, means  7044/ 8584/ 8578
//   light 01/02/03 = 26869/70196/70217 bytes, means 61725/60666/60668
// Previous derivation (2026-07-17, 680x460, when 01 gained the empty-query
// cheat sheet): dark means 7149/8338/8329, light 61601/61052/61055.
// A dead/black webview captures near mean 0; solid white near 65535. The
// light theme sits ~61k, so its band must clear 60k yet still reject a
// blank-white window; the size floors do the rest (a flat rectangle
// compresses to a few hundred bytes). The 01 floors also assert the
// cheat sheet actually rendered: a summoned-but-empty bar compresses to
// ~3.6k (dark) / ~7.4k (light), well under the 10000/12000 floors.
interface ThemeSpec {
  name: string;
  meanMin: number;
  meanMax: number;
  floors: Record<string, number>;
}
const themes: ThemeSpec[] = [
  {
    name: "dark",
    meanMin: 500,
    meanMax: 60000,
    floors: { "01-summoned.png": 10000, "02-results.png": 10000, "03-selection.png": 10000 },
  },
  {
    name: "light",
    meanMin: 30000,
    meanMax: 64000,
    floors: { "01-summoned.png": 12000, "02-results.png": 12000, "03-selection.png": 12000 },
  },
];

const shotRoot = path.join(workspace, "screenshots");

async function findBarWindow(): Promise<string | undefined> {
  const out = await $`xwininfo -root -tree`.silent().nothrow();
  const m = /^\s*(0x[0-9a-f]+)\s+"competent-search-thing".*\s780x550\+/m.exec(String(out.stdout));
  return m?.[1];
}

async function mapState(wid: string): Promise<string> {
  const out = await $`xwininfo -id ${wid}`.silent().nothrow();
  const m = /Map State:\s+(\S+)/.exec(String(out.stdout));
  return m?.[1] ?? "unknown";
}

async function capture(wid: string, theme: ThemeSpec, name: string): Promise<void> {
  const file = path.join(shotRoot, theme.name, name);
  await $`import -window ${wid} ${file}`.silent();
  const size = fs.statSync(file).size;
  const mean = parseFloat((await $`identify -format %[mean] ${file}`.silent()).stdout.trim());
  core.info(`captured ${theme.name}/${name}: ${size} bytes, mean ${mean.toFixed(0)}`);
  const minBytes = theme.floors[name] ?? 2500;
  if (!(mean > theme.meanMin && mean < theme.meanMax))
    throw new Error(`${theme.name}/${name} looks blank (mean ${mean}, want ${theme.meanMin}..${theme.meanMax})`);
  if (size < minBytes)
    throw new Error(`${theme.name}/${name} suspiciously small (${size} bytes < ${minBytes})`);
}

async function launchApp(extraEnv: Record<string, string>): Promise<Proc> {
  const app = spawnLogged("app", bin, [], { COMPETENT_SEARCH_CONFIG_DIR: cfgDir, ...extraEnv });
  await pollFor("hotkey registration + initial index", 20000, 250, async () => {
    if (app.child.exitCode !== null) throw new Error(`app exited early (code ${app.child.exitCode}): ${app.log}`);
    if (/hotkey: registering .* failed/.test(app.log)) throw new Error(`hotkey grab refused: ${app.log}`);
    return /hotkey: .* summons the searchbar/.test(app.log) && /index: \d+ entries in/.test(app.log)
      ? true
      : undefined;
  });
  return app;
}

async function summonAndCapture(theme: ThemeSpec): Promise<void> {
  fs.mkdirSync(path.join(shotRoot, theme.name), { recursive: true });
  // Park the cursor over where the query row will be, so it does not
  // hover-select a result row (and the bar opens on this display).
  await $`xdotool mousemove 640 140`.silent();
  await $`xdotool key --clearmodifiers alt+space`.silent();
  const wid = await pollFor("bar window to map", 15000, 500, async () => {
    const w = await findBarWindow();
    return w !== undefined && (await mapState(w)) === "IsViewable" ? w : undefined;
  });
  await sleep(1000); // let the webview paint
  await capture(wid, theme, "01-summoned.png");

  await $`xdotool windowactivate ${wid}`.silent().nothrow();
  await sleep(400);
  await $`xdotool type --delay 60 rep`.silent();
  await sleep(700);
  await capture(wid, theme, "02-results.png");

  await $`xdotool key Down Down`.silent();
  await sleep(400);
  await capture(wid, theme, "03-selection.png");

  // Escape must hide the bar -- a real behavior assertion, not just a shot.
  await $`xdotool key Escape`.silent();
  await pollFor("bar to hide on Escape", 10000, 500, async () =>
    (await mapState(wid)) === "IsUnMapped" ? true : undefined,
  );
}

try {
  spawnLogged("Xvfb", "Xvfb", [":99", "-screen", "0", "1280x800x24"], {});
  await pollFor("Xvfb to accept connections", 15000, 500, async () => {
    const out = await $`xdpyinfo -display :99`.silent().nothrow();
    return out.exitCode === 0 ? true : undefined;
  });
  spawnLogged("openbox", "openbox", ["--config-file", rcPath], {});
  await sleep(500);

  for (const theme of themes) {
    writeConfig(theme.name);
    let app = await launchApp({});
    try {
      try {
        await summonAndCapture(theme);
      } catch (err) {
        const msg = err instanceof Error ? err.message : String(err);
        core.warning(
          `${theme.name}: first capture attempt failed (${msg}); retrying with WEBKIT_DISABLE_DMABUF_RENDERER=1`,
        );
        core.info(`app log so far:\n${app.log}`);
        await stop(app);
        app = await launchApp({ WEBKIT_DISABLE_DMABUF_RENDERER: "1" });
        await summonAndCapture(theme);
      }
    } finally {
      await stop(app); // fresh process per theme; Xvfb/openbox stay up
    }
  }
  core.info(`screenshots written to ${shotRoot}`);
} catch (err) {
  const msg = err instanceof Error ? err.message : String(err);
  for (const p of procs) {
    const tail = p.log.split("\n").slice(-25).join("\n");
    if (tail.trim() !== "") core.info(`--- ${p.name} log tail ---\n${tail}`);
  }
  const tree = await $`xwininfo -root -tree`.silent().nothrow();
  core.info(`--- window tree ---\n${String(tree.stdout).split("\n").slice(0, 30).join("\n")}`);
  core.setFailed(`screenshots: ${msg}`);
} finally {
  for (const p of [...procs].reverse()) await stop(p);
}
