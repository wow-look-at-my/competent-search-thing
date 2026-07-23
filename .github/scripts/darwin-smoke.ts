// macOS GUI smoke gate (HARD-GATING: any SMOKE FAIL fails the job): boots the
// freshly built app on the runner's real WindowServer session and verifies,
// with hard evidence in the job log, that (a) the app boots and its IPC
// socket answers the JSON protocol, (b) `show` makes a real on-screen window
// appear (CGWindowList probe), (c) IPC toggle/show round-trips answer within
// a deadline EVEN WHILE a large index build is provably running (the
// B-midindex-window check gates on that proof) -- the exact user-reported
// failure was `toggle` timing out with "read unix ...sock: i/o timeout"
// during startup indexing, with no window ever appearing -- (d) the
// deleted legacy v1 line protocol is REJECTED: a bare-word request must earn
// the JSON invalid-request error, never a raw "ok" (a8-legacy-rejected) --
// and (e) once the big build completes, the watch layer comes up on the
// FSEvents whole-filesystem backend (B-backend) with the process's open-fd
// count far below kern.maxfilesperproc (B-fd-headroom) -- the regression
// pins for the macOS field incident where unbounded per-directory kqueue
// watches pinned the process at its fd ceiling and broke every later
// open()/exec.
//
// Verdicts and evidence go to the JOB LOG. Every "SMOKE PASS: <id>" /
// "SMOKE FAIL: <id>" line is a hard check -- there are no warn-and-continue
// ids -- while screenshots are best-effort EVIDENCE (logged as "evidence:",
// never a SMOKE id; copied to smoke-shots/ at the workspace root, which
// ci.yml uploads as the darwin-smoke artifact, mirroring the linux job's
// screenshots artifact). App logs dump at teardown prefixed applog[A]| /
// applog[B]| -- in full when anything failed, filtered to the
// summary-relevant lines on an all-green run.
//
// Runs via wow-look-at-my/actions@typescript#latest (file: input). Injected
// globals used: core, $, fs, path, os, child_process, env.
//
// Runner capability assumptions (macos-latest):
//   - swiftc (Xcode) compiles the CGWindowList probe; missing = fatal, the
//     window checks cannot run without it.
//   - /usr/bin/nc (BSD netcat, -U unix sockets) for raw IPC; a node
//     net.connect subprocess is the fallback when nc is absent.
//   - screencapture may be TCC-restricted; the screenshot is best-effort
//     evidence and NEVER a FAIL.
//   - CGWindowListCopyWindowInfo needs no Screen Recording permission for
//     pid/bounds/layer/alpha (only window NAMES are gated); the probe reads
//     no names.

const workspace = env.GITHUB_WORKSPACE ?? process.cwd();
const stepStart = Date.now();

// ---- deadlines and verdict constants (all polls are hard-capped) ------------
const BOOT_DEADLINE_MS = 30000; // boot -> socket file exists + ping answers "ok"
const IDLE_INDEX_DEADLINE_MS = 60000; // scenario A: the ~200-file index must complete
const RAW_RTT_MS = 2000; // raw socket round-trip budget (version/ping checks)
const CLI_RTT_MS = 2500; // `<bin> toggle|show|hide` must exit within this
const WINDOW_DEADLINE_MS = 8000; // idle-scenario window appear/disappear budget
const FPS_METER_MS = 12000; // a4: first meter report ~2.5s after the bar shows, wide margin
const WINDOW_DEADLINE_BUSY_MS = 10000; // scenario B window budget while indexing
const MIDINDEX_WINDOW_MS = 20000; // scenario B: progress line must appear within this
const B_INDEX_DONE_MS = 180000; // scenario B: the big index must then COMPLETE within this
const WATCH_BACKEND_MS = 30000; // after completion, the watch backend line must land
// The scenario B cap was 180s while B ended mid-index; it now waits
// out the big build for the watch-layer gates (B-backend,
// B-fd-headroom -- the macOS field-incident regression pins), so the
// cap covers boot + mid-index checks + B_INDEX_DONE_MS + the watch
// gates with margin. Still a hard kill.
const SCENARIO_B_CAP_MS = 360000; // hard cap on ALL of scenario B, then SIGKILL
const STOP_GRACE_MS = 5000; // SIGTERM -> SIGKILL grace on app teardown
const POLL_MS = 250; // generic poll interval
const WINDOW_POLL_MS = 400; // window probe interval (each probe execs winlist)
const BOOT_PING_MS = 1000; // per-probe ping budget inside the boot poll
const WINLIST_EXEC_MS = 5000; // per-invocation budget for the window probe binary
// toggleGap is 250ms in internal/app/window.go: a toggle that finds the bar
// hidden within toggleGap of the last Hide is deliberately DROPPED (it is
// treated as the dismiss-press echo), so consecutive toggle checks must wait
// out the gap or a6 would flakily never re-show.
const TOGGLE_CLEAR_MS = 600;
// "Bar window present" = any on-screen window of the app pid at least this
// big. The bar is 780x550 by default; WebKit may add tiny helper windows --
// the size filter drops them.
const MIN_BAR_W = 300;
const MIN_BAR_H = 200;

// Log lines from internal/app/app.go:
//   progress:  "index: indexing... %d entries"
//   complete:  "index: %d entries in %s"
const INDEX_DONE_RE = /index: \d+ entries in [^\n]*/;
const INDEX_PROGRESS_RE = /index: indexing\.\.\. \d+ entries/;

const sleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

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

// ---- check verdicts ----------------------------------------------------------
const failures: string[] = [];
let fatal = false; // an abort outside the per-check verdicts (setFailed in the catch)

async function check(id: string, fn: () => Promise<string>): Promise<boolean> {
  const t0 = Date.now();
  try {
    const detail = await fn();
    core.info(`SMOKE PASS: ${id} (${Date.now() - t0}ms${detail !== "" ? `, ${detail}` : ""})`);
    return true;
  } catch (err) {
    failures.push(id);
    core.error(`SMOKE FAIL: ${id} (${Date.now() - t0}ms): ${errMsg(err)}`);
    return false;
  }
}

// ---- locate the built binary ------------------------------------------------
// go-toolchain matrix naming in CI vs a plain local run; run a throwaway copy,
// never the build/ artifact itself (it rides the publish hand-off).
const candidates = [
  path.join(workspace, "build", "competent-search-thing_darwin_arm64"),
  path.join(workspace, "build", "competent-search-thing"),
];
const builtBin = candidates.find((p) => fs.existsSync(p));
if (builtBin === undefined) {
  const buildDir = path.join(workspace, "build");
  const listing = fs.existsSync(buildDir) ? fs.readdirSync(buildDir).join(", ") : "(no build/ directory)";
  core.setFailed(
    `darwin-smoke: built binary not found (tried ${candidates.join(", ")}); build/ contains: ${listing}. ` +
      "The go-toolchain step must run first and GOFLAGS must include the desktop,production tags.",
  );
  return;
}
const work = fs.mkdtempSync(path.join(os.tmpdir(), "darwin-smoke-"));
const bin = path.join(work, "competent-search-thing");
fs.copyFileSync(builtBin, bin);
fs.chmodSync(bin, 0o755);
core.info(`binary: ${builtBin} (${fs.statSync(builtBin).size} bytes) -> throwaway copy ${bin}`);

// ---- raw IPC transport --------------------------------------------------------
// One request per connection, one line each way. JSON is the ONLY wire shape
// (write '{"cmd":"ping"}\n', read one JSON object line); the pre-JSON
// bare-word v1 shape is deleted and must be rejected with the JSON
// invalid-request error -- a8-legacy-rejected CI-enforces that deletion. The
// server closes the conn after the reply (and after a 2s server-side
// deadline when wedged -- an empty reply here is exactly the user-reported
// failure signature).
const haveNC = fs.existsSync("/usr/bin/nc");
const NODE_SEND_SRC =
  'const net = require("net");' +
  "const sockPath = process.argv[1]; const cmd = process.argv[2];" +
  "const c = net.connect(sockPath);" +
  'let buf = "";' +
  'c.on("connect", () => { c.write(cmd + "\\n"); });' +
  'c.on("data", (d) => { buf += String(d); const i = buf.indexOf("\\n");' +
  ' if (i >= 0) { process.stdout.write(buf.slice(0, i) + "\\n"); c.destroy(); process.exit(0); } });' +
  'c.on("error", (e) => { process.stderr.write(String(e)); process.exit(1); });' +
  'c.on("close", () => { process.exit(1); });';
core.info(`raw IPC transport: ${haveNC ? "/usr/bin/nc -U" : "node net.connect fallback (nc not found)"}`);

interface ExecFailure {
  status?: number | null;
  signal?: string | null;
  stdout?: string;
  stderr?: string;
  code?: string;
  message?: string;
}

interface RawReply {
  reply: string; // trimmed first reply line; "" when none arrived
  ms: number;
  detail: string; // human evidence line for PASS/FAIL details
}

function rawSend(sock: string, cmd: string, timeoutMs: number): RawReply {
  const t0 = Date.now();
  const file = haveNC ? "/usr/bin/nc" : process.execPath;
  const args = haveNC ? ["-U", sock] : ["-e", NODE_SEND_SRC, sock, cmd];
  try {
    const out = child_process.execFileSync(file, args, {
      input: cmd + "\n",
      timeout: timeoutMs,
      killSignal: "SIGKILL",
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"],
    });
    const ms = Date.now() - t0;
    const reply = out.split("\n")[0].trim();
    return { reply, ms, detail: `${cmd} -> "${reply}" in ${ms}ms` };
  } catch (err) {
    const e = err as ExecFailure;
    const ms = Date.now() - t0;
    const got = String(e.stdout ?? "").split("\n")[0].trim();
    const why = e.code === "ETIMEDOUT" ? `no reply within ${timeoutMs}ms (killed)` : `exit=${e.status ?? "?"} signal=${e.signal ?? "-"}`;
    const errOut = String(e.stderr ?? "").trim();
    return {
      reply: got,
      ms,
      detail: `${cmd} -> ${why} in ${ms}ms${got !== "" ? ` partial="${got}"` : ""}${errOut !== "" ? ` stderr=${errOut}` : ""}`,
    };
  }
}

// jsonSend: one JSON (v2) exchange -- the request is {"cmd":<cmd>} and the
// reply must parse as a JSON object (obj undefined = no/non-JSON reply; the
// RawReply carries the evidence either way).
interface JsonReply {
  obj: Record<string, unknown> | undefined;
  raw: RawReply;
}

function jsonSend(sock: string, cmd: string, timeoutMs: number): JsonReply {
  const raw = rawSend(sock, JSON.stringify({ cmd }), timeoutMs);
  try {
    const parsed: unknown = JSON.parse(raw.reply);
    if (typeof parsed === "object" && parsed !== null) return { obj: parsed as Record<string, unknown>, raw };
    return { obj: undefined, raw };
  } catch (err) {
    return { obj: undefined, raw };
  }
}

// ---- app lifecycle -------------------------------------------------------------
interface App {
  name: string;
  proc: import("child_process").ChildProcess;
  sock: string;
  cfgDir: string;
  logFile: string;
  env: Record<string, string | undefined>;
}
const apps: Array<[string, App]> = [];

function startApp(name: string, roots: string[], extraCfg: Record<string, unknown> = {}): App {
  const cfgDir = fs.mkdtempSync(path.join(os.tmpdir(), `css-cfg-${name}-`));
  // Minimal valid config; Load normalizes everything else (custom roots
  // survive the rootsVersion migration untouched -- only the legacy home
  // default gets rewritten). rescanIntervalMinutes 0 = no interval rescans.
  // extraCfg lets a scenario opt into more (the translucent evidence run).
  fs.writeFileSync(
    path.join(cfgDir, "config.json"),
    JSON.stringify({ roots, hotkey: "alt+space", rescanIntervalMinutes: 0, ...extraCfg }, null, 2),
  );
  // SHORT socket path: darwin sun_path caps at ~104 bytes and $TMPDIR on
  // macOS is a long /var/folders/... path, so use /tmp explicitly.
  const sock = `/tmp/css-${name}-${process.pid}.sock`;
  fs.rmSync(sock, { force: true });
  const logFile = path.join(work, `app-${name}.log`);
  const appEnv: Record<string, string | undefined> = {
    ...process.env,
    COMPETENT_SEARCH_CONFIG_DIR: cfgDir,
    COMPETENT_SEARCH_SOCKET: sock,
    // The dev-only fps meter (internal/app fps.go + fpsmeter.ts):
    // a4-fps-meter hard-gates that the whole chain -- env knob, bound
    // methods, rAF loop, report, Go log line -- works end to end on a
    // real WindowServer session. ABSOLUTE rates are evidence only:
    // this is an AC-powered VM with a virtual display and no Low
    // Power Mode; battery behavior is not reproducible in CI.
    COMPETENT_SEARCH_FPS: "1",
    // Keep the automatic login-service registration (internal/app
    // service.go) off the runner: the mac VM has a real gui domain,
    // so without the gate every boot would write a LaunchAgent plist
    // into the runner user's ~/Library/LaunchAgents.
    COMPETENT_SEARCH_NO_SERVICE: "1",
    // Keep the automatic optimal-watch setup (internal/watchsetup)
    // off. fanotify is Linux-only, so this is a no-op on macOS, but
    // set it for parity and to guarantee no privilege prompt ever runs
    // in CI.
    COMPETENT_SEARCH_NO_WATCH_SETUP: "1",
  };
  const fd = fs.openSync(logFile, "a");
  // Zero args = the GUI path (internal/cli bare invocation).
  const proc = child_process.spawn(bin, [], { env: appEnv, detached: false, stdio: ["ignore", fd, fd] });
  fs.closeSync(fd);
  proc.on("error", (err: Error) => fs.appendFileSync(logFile, `spawn error: ${err.message}\n`));
  core.info(`[${name}] app pid ${proc.pid ?? "?"}, socket ${sock}, config ${cfgDir}, log ${logFile}`);
  return { name, proc, sock, cfgDir, logFile, env: appEnv };
}

function readLog(app: App): string {
  try {
    return fs.readFileSync(app.logFile, "utf8");
  } catch (err) {
    return "";
  }
}

function logTail(app: App, lines: number): string {
  return readLog(app).split("\n").slice(-lines).join("\n");
}

async function stopApp(app: App): Promise<void> {
  const p = app.proc;
  if (p.exitCode !== null || p.signalCode !== null) return;
  p.kill("SIGTERM");
  const gone = await Promise.race([
    new Promise<boolean>((resolve) => p.once("exit", () => resolve(true))),
    sleep(STOP_GRACE_MS).then(() => false),
  ]);
  if (!gone) p.kill("SIGKILL");
}

function killNow(app: App): void {
  const p = app.proc;
  if (p.exitCode === null && p.signalCode === null) p.kill("SIGKILL");
}

// cliSend: run `<bin> toggle|show|hide` against the scenario's socket (env
// carries COMPETENT_SEARCH_SOCKET + CONFIG_DIR). SIGKILL on timeout so a hung
// child -- including a toggle/show that decided to boot its own GUI because
// the socket died -- can never wedge the step. Exit code + wall ms are the
// round-trip evidence.
interface CliResult {
  code: number | null;
  signal: string | null;
  ms: number;
  stderr: string;
}

function cliSend(app: App, sub: string): CliResult {
  const t0 = Date.now();
  try {
    child_process.execFileSync(bin, [sub], {
      env: app.env,
      timeout: CLI_RTT_MS,
      killSignal: "SIGKILL",
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    return { code: 0, signal: null, ms: Date.now() - t0, stderr: "" };
  } catch (err) {
    const e = err as ExecFailure;
    const extra = e.code !== undefined && e.code !== "" ? ` code=${e.code}` : "";
    return {
      code: e.status ?? null,
      signal: e.signal ?? null,
      ms: Date.now() - t0,
      stderr: `${String(e.stderr ?? "").trim()}${extra}`,
    };
  }
}

function assertCli(app: App, sub: string): string {
  const r = cliSend(app, sub);
  if (r.code !== 0) {
    throw new Error(
      `\`${sub}\` failed: exit=${r.code ?? "none"} signal=${r.signal ?? "-"} in ${r.ms}ms (budget ${CLI_RTT_MS}ms)` +
        `${r.stderr !== "" ? `; stderr: ${r.stderr}` : ""}`,
    );
  }
  return `\`${sub}\` exit 0 in ${r.ms}ms`;
}

// ---- window probe (CGWindowList via a tiny compiled Swift tool) ---------------
const WINLIST_SWIFT =
  [
    "import CoreGraphics",
    "import Foundation",
    "let pid = Int32(CommandLine.arguments[1])!",
    "let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as? [[String: Any]] ?? []",
    "var n = 0",
    "for w in list {",
    "  if let p = w[kCGWindowOwnerPID as String] as? Int32, p == pid {",
    "    let b = w[kCGWindowBounds as String] as? [String: NSNumber] ?? [:]",
    '    print("WIN pid=\\(p) x=\\(b["X"] ?? -1) y=\\(b["Y"] ?? -1) w=\\(b["Width"] ?? -1) h=\\(b["Height"] ?? -1) layer=\\(w[kCGWindowLayer as String] as? Int ?? -999) alpha=\\(w[kCGWindowAlpha as String] as? Double ?? -1)")',
    "    n += 1",
    "  }",
    "}",
    'print("COUNT=\\(n)")',
  ].join("\n") + "\n";

const winlistSrc = path.join(work, "winlist.swift");
const winlistBin = path.join(work, "winlist");

interface WinProbe {
  ok: boolean; // the probe binary ran; false = indeterminate, keep polling
  raw: string;
  barPresent: boolean;
}

function probeWindows(appPid: number): WinProbe {
  try {
    const out = child_process.execFileSync(winlistBin, [String(appPid)], {
      timeout: WINLIST_EXEC_MS,
      killSignal: "SIGKILL",
      encoding: "utf8",
      stdio: ["ignore", "pipe", "pipe"],
    });
    let present = false;
    for (const line of out.split("\n")) {
      if (!line.startsWith("WIN ")) continue;
      const m = /w=([-\d.]+) h=([-\d.]+)/.exec(line);
      if (m !== null && parseFloat(m[1]) >= MIN_BAR_W && parseFloat(m[2]) >= MIN_BAR_H) present = true;
    }
    return { ok: true, raw: out.trim(), barPresent: present };
  } catch (err) {
    const e = err as ExecFailure;
    return {
      ok: false,
      raw: `winlist probe failed: exit=${e.status ?? "?"} signal=${e.signal ?? "-"} ${String(e.stderr ?? "").trim()}`,
      barPresent: false,
    };
  }
}

// expectWindow polls until the bar window is present (want=true) or gone
// (want=false), logging the probe's raw output exactly once -- when the
// verdict is decided -- never per poll tick.
async function expectWindow(app: App, want: boolean, deadlineMs: number): Promise<string> {
  const t0 = Date.now();
  const wantLabel = want ? "present" : "gone";
  let last: WinProbe = { ok: false, raw: "(no probe ran)", barPresent: false };
  try {
    await pollFor(`bar window ${wantLabel} (>= ${MIN_BAR_W}x${MIN_BAR_H})`, deadlineMs, WINDOW_POLL_MS, async () => {
      last = probeWindows(app.proc.pid ?? -1);
      if (!last.ok) return undefined; // probe hiccup: indeterminate, retry
      return last.barPresent === want ? true : undefined;
    });
  } catch (err) {
    core.info(`window probe at verdict (want ${wantLabel}, pid ${app.proc.pid ?? "?"}):\n${last.raw}`);
    throw err;
  }
  core.info(`window probe at verdict (want ${wantLabel}, pid ${app.proc.pid ?? "?"}):\n${last.raw}`);
  return `window ${wantLabel} after ${Date.now() - t0}ms`;
}

// ---- shared boot check ---------------------------------------------------------
// Boot = the socket answers a JSON ping with {"ok":true}, so every boot also
// asserts the v2 protocol shape.
function bootCheck(id: string, app: App): Promise<boolean> {
  return check(id, () =>
    pollFor(`ipc socket ${app.sock} to answer a JSON ping`, BOOT_DEADLINE_MS, POLL_MS, async () => {
      if (app.proc.exitCode !== null) {
        throw new Error(`app exited early (code ${app.proc.exitCode}); log tail:\n${logTail(app, 25)}`);
      }
      if (!fs.existsSync(app.sock)) return undefined;
      const r = jsonSend(app.sock, "ping", BOOT_PING_MS);
      return r.obj !== undefined && r.obj.ok === true ? `json ping ok in ${r.raw.ms}ms` : undefined;
    }),
  );
}

// ---- scenario A: idle (small fixture, index completes, then drive the bar) ----
function makeFixtureTree(): string {
  const fixture = fs.mkdtempSync(path.join(os.tmpdir(), "css-fixture-"));
  const dirs = [
    "Documents/Reports",
    "Projects/searchbar",
    "Projects/webapp/src",
    "Pictures/Screenshots",
    "Music/Playlists",
    "Videos",
    "Downloads",
  ];
  for (const d of dirs) fs.mkdirSync(path.join(fixture, d), { recursive: true });
  const files = [
    "README.md",
    "notes.txt",
    "todo.md",
    "Documents/quarterly-report-2026.pdf",
    "Documents/resume.docx",
    "Projects/searchbar/main.go",
    "Projects/webapp/src/app.ts",
  ];
  for (let i = 1; i <= 60; i++) files.push(`Downloads/archive-item-${String(i).padStart(2, "0")}.dat`);
  for (let i = 1; i <= 50; i++) files.push(`Music/track-${String(i).padStart(2, "0")}.mp3`);
  for (let i = 1; i <= 50; i++) files.push(`Pictures/photo-${String(i).padStart(2, "0")}.jpg`);
  for (let i = 1; i <= 40; i++) files.push(`Documents/Reports/report-${String(i).padStart(2, "0")}.md`);
  for (const f of files) fs.writeFileSync(path.join(fixture, f), "");
  return fixture; // ~207 files across 7 dirs, deterministic
}

async function screenshotBestEffort(name: string): Promise<void> {
  // EVIDENCE CAPTURE, not a requirement check: screencapture is TCC-gated on
  // some runner images, so a screenshot can never be required. Deliberately
  // logged as "evidence:" with NO SMOKE id -- every remaining SMOKE id is a
  // hard pass/fail, and nothing here can soft-pass one. Successful captures
  // are also copied to smoke-shots/ at the workspace root, which ci.yml
  // uploads as the darwin-smoke artifact (the linux screenshots pattern).
  const t0 = Date.now();
  const file = path.join(work, name);
  try {
    const cap = await $`screencapture -x ${file}`.silent().nothrow();
    if (cap.exitCode !== 0 || !fs.existsSync(file)) {
      core.info(`evidence: screenshot ${name} unavailable (exit=${cap.exitCode} ${String(cap.stderr).trim()})`);
      return;
    }
    const size = fs.statSync(file).size;
    const sips = await $`sips -g pixelWidth -g pixelHeight ${file}`.silent().nothrow();
    const dims = String(sips.stdout).trim().replace(/\s+/g, " ");
    const shotsDir = path.join(workspace, "smoke-shots");
    fs.mkdirSync(shotsDir, { recursive: true });
    fs.copyFileSync(file, path.join(shotsDir, name));
    core.info(`evidence: screenshot ${name} captured (${Date.now() - t0}ms, ${size} bytes, ${dims}) -> smoke-shots/${name}`);
  } catch (err) {
    core.info(`evidence: screenshot ${name} unavailable: ${errMsg(err)}`);
  }
}

async function scenarioA(): Promise<void> {
  core.info("---- scenario A: idle (small fixture index) ----");
  const fixture = makeFixtureTree();
  const app = startApp("idle", [fixture]);
  apps.push(["A", app]);

  if (!(await bootCheck("A-boot", app))) return;

  await check("A-index", () =>
    pollFor("index completion in the app log", IDLE_INDEX_DEADLINE_MS, 500, async () => {
      if (app.proc.exitCode !== null) throw new Error(`app exited (code ${app.proc.exitCode})`);
      const m = INDEX_DONE_RE.exec(readLog(app));
      return m !== null ? `"${m[0].trim()}"` : undefined;
    }),
  );

  await check("a1-version", async () => {
    const r = jsonSend(app.sock, "version", RAW_RTT_MS);
    if (r.obj === undefined) throw new Error(`reply is not a JSON object: ${r.raw.detail}`);
    if (r.obj.ok !== true || typeof r.obj.version !== "string" || r.obj.version === "") {
      throw new Error(`want {"ok":true,"version":"<non-empty>"}: ${r.raw.detail}`);
    }
    return r.raw.detail;
  });

  // The legacy v1 line protocol is DELETED, CI-enforced: a bare-word request
  // must be rejected with the JSON invalid-request error (or the conn closed
  // with no reply) -- the old raw "ok" answer is now a FAIL.
  await check("a8-legacy-rejected", async () => {
    const r = rawSend(app.sock, "ping", RAW_RTT_MS);
    if (r.reply === "ok") {
      throw new Error(`bare "ping" earned the legacy raw "ok" -- the v1 line protocol must be gone: ${r.detail}`);
    }
    if (r.reply === "") {
      // No reply line at all is acceptable only as a clean close; a hang
      // until the timeout kill is the wedged-server signature, not a
      // rejection.
      if (r.detail.includes("no reply within")) {
        throw new Error(`bare "ping" hung instead of being rejected: ${r.detail}`);
      }
      return `conn closed with no reply; ${r.detail}`;
    }
    let ok: unknown;
    try {
      const parsed: unknown = JSON.parse(r.reply);
      if (typeof parsed !== "object" || parsed === null) throw new Error("not a JSON object");
      ok = (parsed as Record<string, unknown>).ok;
    } catch (err) {
      throw new Error(`bare "ping" must earn a JSON error reply (or no reply at all): ${r.detail}`);
    }
    if (ok !== false) throw new Error(`bare "ping" must be rejected with {"ok":false,...}: ${r.detail}`);
    return r.detail;
  });

  await check("a2-show", async () => assertCli(app, "show"));
  await check("a3-window-appears", () => expectWindow(app, true, WINDOW_DEADLINE_MS));

  // NO screencapture before a4/a5: on the macOS 26 runner image the job's
  // FIRST screencapture can pop the TCC screen-recording consent dialog
  // ("bash is requesting to ... directly access your screen"), a system
  // modal that steals key status from the bar. The webview then fires
  // blur and the app's (deliberate, Spotlight-style) blur auto-hide hides
  // the bar -- starving a4's rAF accumulation (a hidden WKWebView services
  // no frames) and inverting a5's toggle into a re-SHOW. Proven by run
  // 29728632030 (head 464438e): 02-reshown-macos.png captured the dialog,
  // a4+a5 failed exactly that way while every focus-independent gate
  // passed. The race is per-VM, so the ONLY safe ordering is to keep every
  // capture behind the focus/visibility-sensitive hard gates; scenario A's
  // one evidence shot now lands after a6 re-shows the bar (same summoned
  // state), where the remaining gates (a7 hide, a9 hidden-IPC ack) are
  // dialog-tolerant -- run 29728632030 itself proved a7/a9 and all of
  // scenario B green with the dialog on screen.

  // a4-fps-meter: with COMPETENT_SEARCH_FPS=1 and the bar visible, a
  // parseable fps summary line must land in the app log (the meter's
  // first report fires after ~2.5s of visible rAF time), and the
  // startup context line must be present too -- it proves the darwin
  // display/power probe (NSScreen/NSProcessInfo cgo) compiled and ran.
  // The gate is the MECHANISM, never the absolute number (AC VM).
  await check("a4-fps-meter", () =>
    pollFor("an fps summary line in the app log", FPS_METER_MS, POLL_MS, async () => {
      if (app.proc.exitCode !== null) throw new Error(`app exited (code ${app.proc.exitCode})`);
      const log0 = readLog(app);
      const ctx = /fps: meter on; display \d+Hz max, lowPowerMode=(on|off), thermalState=\w+/.exec(log0);
      if (ctx === null) {
        if (/fps: meter on;/.test(log0)) {
          throw new Error(`fps context line present but unparseable (darwin power probe broken): ${/fps: meter on;[^\n]*/.exec(log0)?.[0] ?? ""}`);
        }
        return undefined;
      }
      const m = /fps: (\d+(?:\.\d+)?) avg, (\d+(?:\.\d+)?) max, (\d+)% frames >20ms over [\d.]+s \(rAF ~(\d+)Hz\)/.exec(log0);
      return m !== null ? `"${ctx[0]}"; "${m[0]}" (absolute rate is informational: AC-powered CI VM)` : undefined;
    }),
  );

  await check("a5-toggle-hides", async () => {
    const d = assertCli(app, "toggle");
    return `${d}; ${await expectWindow(app, false, WINDOW_DEADLINE_MS)}`;
  });

  await sleep(TOGGLE_CLEAR_MS); // clear toggleGap, see the constant's comment
  await check("a6-toggle-reshows", async () => {
    const d = assertCli(app, "toggle");
    return `${d}; ${await expectWindow(app, true, WINDOW_DEADLINE_MS)}`;
  });
  // Scenario A's one evidence shot: the a6-reshown bar IS the summoned
  // state (01 keeps its docs-referenced name). Placed here, after the
  // focus-sensitive gates, per the TCC-dialog note above a4.
  await screenshotBestEffort("01-summoned-macos.png");

  await check("a7-hide", async () => {
    const d = assertCli(app, "hide");
    return `${d}; ${await expectWindow(app, false, WINDOW_DEADLINE_MS)}`;
  });

  // The config command is acked like every other cmd (ack-first: the
  // summon-into-editor runs after the reply; the frontend editor lands in
  // Phase C, so this is an IPC-ack check, not a UI check). Sent while
  // hidden, after -- and clear of -- the toggle pair; the explicit hide
  // restores the hidden state a7 left, so teardown sees no change.
  await check("a9-config-ipc", async () => {
    const r = jsonSend(app.sock, "config", RAW_RTT_MS);
    if (!r.obj || r.obj.ok !== true || r.obj.accepted !== "config") {
      throw new Error(`{"cmd":"config"} not acked: ${r.raw.detail}`);
    }
    const h = jsonSend(app.sock, "hide", RAW_RTT_MS);
    if (!h.obj || h.obj.ok !== true) {
      throw new Error(`restoring hide not acked: ${h.raw.detail}`);
    }
    return `${r.raw.detail}; restored hidden`;
  });

  await stopApp(app);
}

// ---- translucent evidence run (best-effort, NEVER a gate) ---------------------
// Boots one extra app with window.translucent=true (the macOS frosted-glass
// path: Mac.WindowIsTranslucent + WebviewIsTransparent + vibrant appearance)
// and captures a screenshot for the darwin-smoke artifact. EVIDENCE ONLY:
// no SMOKE ids, every failure is logged as "evidence: ... unavailable" and
// swallowed -- the real acceptance test for the frosted look is the user's
// own screen, and screencapture flattens/TCC-blocks on some runner images
// anyway.
async function translucentEvidence(): Promise<void> {
  core.info("---- translucent evidence (best-effort, no gates) ----");
  const t0 = Date.now();
  try {
    const fixture = makeFixtureTree();
    const app = startApp("translucent", [fixture], { window: { translucent: true } });
    apps.push(["T", app]);
    await pollFor("translucent app json ping", BOOT_DEADLINE_MS, POLL_MS, async () => {
      if (app.proc.exitCode !== null) {
        throw new Error(`app exited early (code ${app.proc.exitCode}); log tail:\n${logTail(app, 15)}`);
      }
      if (!fs.existsSync(app.sock)) return undefined;
      const r = jsonSend(app.sock, "ping", BOOT_PING_MS);
      return r.obj !== undefined && r.obj.ok === true ? true : undefined;
    });
    const show = cliSend(app, "show");
    if (show.code !== 0) throw new Error(`show failed: exit=${show.code ?? "none"} ${show.stderr}`);
    await expectWindow(app, true, WINDOW_DEADLINE_MS);
    await screenshotBestEffort("03-translucent-macos.png");
    await stopApp(app);
    core.info(`evidence: translucent boot + capture completed in ${Date.now() - t0}ms`);
  } catch (err) {
    core.info(`evidence: translucent capture unavailable: ${errMsg(err)}`);
  }
}

// ---- scenario B: mid-index (big root; the user-reported failure window) -------
function pickBigRoot(): string {
  try {
    const hits = fs
      .readdirSync("/Applications")
      .filter((n) => n.startsWith("Xcode") && n.endsWith(".app"))
      .sort();
    if (hits.length > 0) return path.join("/Applications", hits[0]);
  } catch (err) {
    // fall through to /usr
  }
  return "/usr";
}

async function scenarioB(): Promise<void> {
  core.info("---- scenario B: mid-index (big root) ----");
  const root = pickBigRoot();
  core.info(`[midx] big root: ${root}`);
  const app = startApp("midx", [root]);
  apps.push(["B", app]);
  const bStart = Date.now();
  const capBail = (): boolean => {
    if (Date.now() - bStart <= SCENARIO_B_CAP_MS) return false;
    failures.push("B-cap");
    core.error(
      `SMOKE FAIL: B-cap (scenario B exceeded its ${SCENARIO_B_CAP_MS}ms hard cap; SIGKILLing the app and reporting what we have)`,
    );
    killNow(app);
    return true;
  };

  if (!(await bootCheck("B-boot", app))) return;

  // HARD GATE on the mid-index window: before b1 runs, the app log must
  // already contain an "index: indexing..." progress line AND not yet the
  // completion line, or scenario B proves nothing about the user-reported
  // mid-index failure. The socket answers before the walk's first progress
  // tick lands in the log (ipc.Listen precedes the GUI boot), so the
  // progress line is polled for briefly -- but a completion line at ANY
  // point before the window is proven fails immediately: with an Xcode.app
  // root a too-fast finish is implausible, so hitting this is a real
  // harness bug to fix (bigger root, tighter boot detection), never a
  // warning to shrug off.
  await check("B-midindex-window", () =>
    pollFor('an "index: indexing..." line with no completion line', MIDINDEX_WINDOW_MS, POLL_MS, async () => {
      if (app.proc.exitCode !== null) throw new Error(`app exited (code ${app.proc.exitCode})`);
      const log0 = readLog(app);
      if (INDEX_DONE_RE.test(log0)) {
        throw new Error(
          `the big index (root ${root}) completed before the mid-index checks began -- the window was missed`,
        );
      }
      return INDEX_PROGRESS_RE.test(log0) ? "progress line present, completion line absent" : undefined;
    }),
  );

  if (capBail()) return;
  // b3 is the exact user-reported failure: on their Mac, `toggle` during
  // startup indexing timed out with "read unix ...sock: i/o timeout" and no
  // window ever appeared.
  await check("b1-show-during-index", async () => assertCli(app, "show"));
  if (capBail()) return;
  await check("b2-window-during-index", () => expectWindow(app, true, WINDOW_DEADLINE_BUSY_MS));
  if (capBail()) return;
  await check("b3-toggle-during-index", async () => assertCli(app, "toggle"));
  if (capBail()) return;
  await check("b4-ping-during-index", async () => {
    const r = jsonSend(app.sock, "ping", RAW_RTT_MS);
    if (r.obj === undefined || r.obj.ok !== true) {
      throw new Error(`want JSON {"ok":true}: ${r.raw.detail}`);
    }
    return r.raw.detail;
  });

  if (capBail()) return;
  // The macOS field-incident regression pins need the watch layer UP,
  // and the watch layer starts only after the initial build completes
  // -- so scenario B waits out the big build (bounded) before the
  // backend and fd-headroom gates. Teardown still SIGTERM/SIGKILLs.
  await check("B-index-done", () =>
    pollFor("big index completion in the app log", B_INDEX_DONE_MS, 500, async () => {
      if (app.proc.exitCode !== null) throw new Error(`app exited (code ${app.proc.exitCode})`);
      const m = INDEX_DONE_RE.exec(readLog(app));
      return m !== null ? `"${m[0].trim()}"` : undefined;
    }),
  );

  if (capBail()) return;
  // B-backend: the honest-label + auto-selection pin in one grep. On
  // macOS, "auto" must resolve to the FSEvents whole-filesystem
  // backend; a kqueue/none line here means the fd-eating
  // per-directory fallback (or no live watching at all) is running --
  // the exact field failure mode (17,704 watched dirs pinned the
  // process at its fd ceiling and broke every later open()/exec).
  await check("B-backend", () =>
    pollFor('a "watch: backend ..." line naming fsevents', WATCH_BACKEND_MS, POLL_MS, async () => {
      if (app.proc.exitCode !== null) throw new Error(`app exited (code ${app.proc.exitCode})`);
      const m = /watch: backend ([a-z]+):/.exec(readLog(app));
      if (m === null) return undefined;
      if (m[1] !== "fsevents") {
        throw new Error(`watch backend is "${m[1]}", want "fsevents" (line: "${m[0]}")`);
      }
      return `"${m[0]}" -- fsevents active`;
    }),
  );

  if (capBail()) return;
  // B-fd-headroom: with fsevents wide coverage the app sits at a few
  // hundred open fds even over a big root; a regressed unbounded
  // kqueue path lands AT the kern.maxfilesperproc ceiling. lsof rows
  // slightly overcount fds (cwd/txt segment rows) -- the threshold
  // absorbs that with a wide margin, and it stays valid even if
  // scenario B's root ever shrinks (it asserts an upper bound;
  // B-backend is the load-bearing selection pin). lsof itself is a
  // separate process, so it works even against a target at its fd
  // ceiling.
  await check("B-fd-headroom", async () => {
    const limit = parseInt(
      child_process.execFileSync("/usr/sbin/sysctl", ["-n", "kern.maxfilesperproc"]).toString().trim(),
      10,
    );
    if (!Number.isFinite(limit) || limit <= 0) {
      throw new Error("cannot read kern.maxfilesperproc");
    }
    const rows =
      child_process
        .execFileSync("/usr/sbin/lsof", ["-n", "-P", "-p", String(app.proc.pid)], { maxBuffer: 64 * 1024 * 1024 })
        .toString()
        .trim()
        .split("\n").length - 1;
    const ceiling = Math.min(5000, Math.floor(limit / 2));
    if (rows > ceiling) {
      throw new Error(`fd headroom gone: lsof rows ${rows} > ceiling ${ceiling} (kern.maxfilesperproc ${limit})`);
    }
    return `lsof rows ${rows} <= ceiling ${ceiling} (kern.maxfilesperproc ${limit})`;
  });
}

// ---- run ----------------------------------------------------------------------
function dumpAppLog(label: string, app: App): void {
  let text: string;
  try {
    text = fs.readFileSync(app.logFile, "utf8");
  } catch (err) {
    text = `(log unreadable: ${errMsg(err)})`;
  }
  const prefixed = text
    .replace(/\n$/, "")
    .split("\n")
    .map((l) => `applog[${label}]|${l}`)
    .join("\n");
  core.info(`---- full app log [${label}] (${app.logFile}) ----\n${prefixed}`);
}

// The lines that make a green run self-explanatory without the full dump:
// hotkey wiring, index build/progress/summary (which includes "startup
// complete"), watch backend state, and anything that panicked.
const SUMMARY_LINE_RE = /hotkey:|index:|watch:|fps:|startup complete|panic/;

function dumpAppLogSummary(label: string, app: App): void {
  const lines = readLog(app)
    .replace(/\n$/, "")
    .split("\n")
    .filter((l) => SUMMARY_LINE_RE.test(l))
    .map((l) => `applog[${label}]|${l}`);
  const body = lines.length > 0 ? lines.join("\n") : `applog[${label}]|(no summary-relevant lines)`;
  core.info(`---- app log summary [${label}] (${app.logFile}; full dump prints only on failure) ----\n${body}`);
}

try {
  // Compile the window probe ONCE; poll loops invoke the binary. Fatal when
  // it cannot be built -- every window check depends on it.
  fs.writeFileSync(winlistSrc, WINLIST_SWIFT);
  const tCompile = Date.now();
  const sc = await $`swiftc -O -o ${winlistBin} ${winlistSrc}`.silent().nothrow();
  if (sc.exitCode !== 0) {
    core.setFailed(
      `darwin-smoke: cannot compile the CGWindowList probe (swiftc missing or broken on this runner), ` +
        `exit=${sc.exitCode}: ${String(sc.stderr).slice(0, 2000)}`,
    );
    return;
  }
  core.info(`winlist probe compiled in ${Date.now() - tCompile}ms`);

  await scenarioA();
  await translucentEvidence();
  await scenarioB();

  if (failures.length > 0) {
    core.setFailed(`darwin-smoke: ${failures.length} check(s) failed: ${failures.join(", ")}`);
  } else {
    core.info("SMOKE: all checks passed");
  }
} catch (err) {
  fatal = true;
  const sofar = failures.length > 0 ? ` (failed checks so far: ${failures.join(", ")})` : "";
  core.setFailed(`darwin-smoke: ${errMsg(err)}${sofar}`);
} finally {
  for (const [, app] of apps) await stopApp(app);
  // App logs are the primary debugging evidence: on ANY failure dump them in
  // full; on an all-green run print only the summary-relevant lines so the
  // gate stays self-explanatory without spamming.
  const allGreen = failures.length === 0 && !fatal;
  for (const [label, app] of apps) {
    if (allGreen) dumpAppLogSummary(label, app);
    else dumpAppLog(label, app);
  }
  const vers = await $`sw_vers`.silent().nothrow();
  core.info(`sw_vers: ${String(vers.stdout).trim().split("\n").join(" | ")}`);
  core.info(`darwin-smoke total wall time: ${Date.now() - stepStart}ms`);
}
