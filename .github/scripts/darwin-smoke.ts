// macOS GUI smoke gate: boots the freshly built app on the runner's real
// WindowServer session and verifies, with hard evidence in the job log, that
// (a) the app boots and its IPC socket answers, (b) `show` makes a real
// on-screen window appear (CGWindowList probe), and (c) IPC toggle/show
// round-trips answer within a deadline EVEN WHILE a large index build is
// running -- the exact user-reported failure was `toggle` timing out with
// "read unix ...sock: i/o timeout" during startup indexing, with no window
// ever appearing.
//
// All evidence goes to the JOB LOG (org rule: no actions/upload-artifact).
// Every check logs "SMOKE PASS: <id> (<ms>ms, detail)" or "SMOKE FAIL: ...";
// the round-trip milliseconds ARE the evidence for the IPC bug. Full app
// logs are dumped at teardown prefixed applog[A]| / applog[B]|.
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
const WINDOW_DEADLINE_BUSY_MS = 10000; // scenario B window budget while indexing
const SCENARIO_B_CAP_MS = 180000; // hard cap on ALL of scenario B, then SIGKILL
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
// The line protocol: write "<cmd>\n", read exactly one reply line ("ok" /
// version string / "err <reason>"); the server closes the conn after the
// reply (and after a 2s server-side deadline when wedged -- an empty reply
// here is exactly the user-reported failure signature).
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

function startApp(name: string, roots: string[]): App {
  const cfgDir = fs.mkdtempSync(path.join(os.tmpdir(), `css-cfg-${name}-`));
  // Minimal valid config; Load normalizes everything else (custom roots
  // survive the rootsVersion migration untouched -- only the legacy home
  // default gets rewritten). rescanIntervalMinutes 0 = no interval rescans.
  fs.writeFileSync(
    path.join(cfgDir, "config.json"),
    JSON.stringify({ roots, hotkey: "alt+space", rescanIntervalMinutes: 0 }, null, 2),
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
function bootCheck(id: string, app: App): Promise<boolean> {
  return check(id, () =>
    pollFor(`ipc socket ${app.sock} to answer ping`, BOOT_DEADLINE_MS, POLL_MS, async () => {
      if (app.proc.exitCode !== null) {
        throw new Error(`app exited early (code ${app.proc.exitCode}); log tail:\n${logTail(app, 25)}`);
      }
      if (!fs.existsSync(app.sock)) return undefined;
      const r = rawSend(app.sock, "ping", BOOT_PING_MS);
      return r.reply === "ok" ? `ping "ok" in ${r.ms}ms` : undefined;
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

async function screenshotBestEffort(): Promise<void> {
  // a4: best-effort evidence, NEVER a FAIL (screencapture is TCC-gated on
  // some runner images). The size + dimensions in the log are the evidence.
  const t0 = Date.now();
  const file = path.join(work, "smoke-a.png");
  try {
    const cap = await $`screencapture -x ${file}`.silent().nothrow();
    if (cap.exitCode !== 0 || !fs.existsSync(file)) {
      core.info(`SMOKE WARN: a4 screenshot unavailable: exit=${cap.exitCode} ${String(cap.stderr).trim()}`);
      return;
    }
    const size = fs.statSync(file).size;
    const sips = await $`sips -g pixelWidth -g pixelHeight ${file}`.silent().nothrow();
    const dims = String(sips.stdout).trim().replace(/\s+/g, " ");
    core.info(`SMOKE PASS: a4-screenshot (${Date.now() - t0}ms, exit=0, ${file}: ${size} bytes, ${dims})`);
  } catch (err) {
    core.info(`SMOKE WARN: a4 screenshot unavailable: ${errMsg(err)}`);
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
    const r = rawSend(app.sock, "version", RAW_RTT_MS);
    if (r.reply === "" || r.reply.startsWith("err")) throw new Error(r.detail);
    return r.detail;
  });

  await check("a2-show", async () => assertCli(app, "show"));
  await check("a3-window-appears", () => expectWindow(app, true, WINDOW_DEADLINE_MS));
  await screenshotBestEffort();

  await check("a5-toggle-hides", async () => {
    const d = assertCli(app, "toggle");
    return `${d}; ${await expectWindow(app, false, WINDOW_DEADLINE_MS)}`;
  });

  await sleep(TOGGLE_CLEAR_MS); // clear toggleGap, see the constant's comment
  await check("a6-toggle-reshows", async () => {
    const d = assertCli(app, "toggle");
    return `${d}; ${await expectWindow(app, true, WINDOW_DEADLINE_MS)}`;
  });

  await check("a7-hide", async () => {
    const d = assertCli(app, "hide");
    return `${d}; ${await expectWindow(app, false, WINDOW_DEADLINE_MS)}`;
  });

  await stopApp(app);
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

  // Prove the big index build is actually in flight before the checks run.
  const log0 = readLog(app);
  if (INDEX_DONE_RE.test(log0)) {
    core.info("SMOKE WARN: big index finished too fast; mid-index timing not exercised");
  } else {
    const evidence = INDEX_PROGRESS_RE.test(log0)
      ? '"index: indexing..." progress line in the log'
      : "no completion line in the log yet";
    core.info(`mid-index confirmed: ${evidence}`);
  }

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
    const r = rawSend(app.sock, "ping", RAW_RTT_MS);
    if (r.reply !== "ok") throw new Error(r.detail);
    return r.detail;
  });

  const endState = INDEX_DONE_RE.test(readLog(app)) ? "completed during the checks" : "still building (as intended)";
  core.info(`index state at scenario B end: ${endState}`);
  // Deliberately do NOT wait for the big index; teardown SIGTERM/SIGKILLs.
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
  await scenarioB();

  if (failures.length > 0) {
    core.setFailed(`darwin-smoke: ${failures.length} check(s) failed: ${failures.join(", ")}`);
  } else {
    core.info("SMOKE: all checks passed");
  }
} catch (err) {
  const sofar = failures.length > 0 ? ` (failed checks so far: ${failures.join(", ")})` : "";
  core.setFailed(`darwin-smoke: ${errMsg(err)}${sofar}`);
} finally {
  for (const [, app] of apps) await stopApp(app);
  // The app logs are the primary debugging evidence for iterating on the
  // macOS summon bug -- dump them in full.
  for (const [label, app] of apps) dumpAppLog(label, app);
  const vers = await $`sw_vers`.silent().nothrow();
  core.info(`sw_vers:\n${String(vers.stdout).trim()}`);
  const csr = await $`csrutil status`.silent().nothrow();
  core.info(`csrutil: ${(String(csr.stdout).trim() || String(csr.stderr).trim()) ?? ""} (exit ${csr.exitCode})`);
  core.info(`darwin-smoke total wall time: ${Date.now() - stepStart}ms`);
}
