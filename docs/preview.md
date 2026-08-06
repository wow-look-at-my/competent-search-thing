# internal/preview

Extracted from CLAUDE.md's architecture map, which keeps a one-line
pointer here. Everything below is that entry, unchanged.

`internal/preview` -- the preview-pane engine, pure (no Wails
imports) and headless-tested. preview.go holds the wire contract:
Target {kind "file"|"plugin"|"none", path, isDir, title, subtitle,
pluginName} and Payload {gen, kind
"meta"|"text"|"image"|"dir"|"web"|"ai"|"error", title, path, meta,
text, image, dir, web, ai, err, durMs}. dispatch.go:
`New(parentCtx, Options{TextMaxKB, ImageMaxEdge, DirMaxEntries,
Emit, KagiAPIKey, KagiBaseURL, KagiMaxResults, AIAPIKey/AIBaseURL/
AIModel/AIMaxOutputTokens, AICachePath, Logf})` -> Dispatcher (the
base URLs go through normalizeBaseURL: empty = the client default,
ONE trailing "/" trimmed, anything not http(s)-with-a-host leaves
that provider UNAVAILABLE -- webErr/aiErr carry the terse
invalid-baseUrl message the fetch path emits, and the URL value is
never logged or emitted because it may carry userinfo);
`Preview(target, gen)` is synchronous bookkeeping only (mutex'd
cancel of the previous request + gen store; kind none/"" =
cancel-only) and spawns ONE goroutine per request; file targets
emit a FAST meta card first, then the rich payload (dir listing /
capped text / thumbnail / a final meta card with a "binary" note)
under per-request hard timeouts (2s meta/dir/text, 4s image, via
runUnder racing the provider against the ctx); symlinks are
described (readlink) and never followed; every emit is suppressed
once the request ctx is cancelled; provider funcs are Dispatcher
seam fields for tests (webFn/aiFn stay nil while the matching key
is unconfigured -- WebConfigured()/AIConfigured() report it).
`FetchWeb(query, gen)` / `FetchAI(query, gen)` are the explicit
web/AI triggers sharing Preview's SAME cancel+generation space (a
fetch supersedes an in-flight file preview and vice versa, via
arm()): exactly ONE payload per accepted fetch -- kind "web"
{query, results, cached} / "ai" {query, answer, model, cached} /
"error" (blank query = "empty query"; missing knobs and invalid
base URLs each name their own: "kagi: no API key
(preview.kagi.apiKey or KAGI_API_KEY)" / "kagi: invalid baseUrl
(preview.kagi.baseUrl)" / "ai: no API base URL
(preview.ai.baseUrl)" / "ai: no model (preview.ai.model)" / "ai:
invalid baseUrl (preview.ai.baseUrl)"; provider
failure; 10s
web / 90s ai hard
timeouts spelled out by fetchErrMsg). aiprovider.go wires the ONE
AI endpoint per Dispatcher (wireAI: baseUrl and model REQUIRED --
there is no provider list and no default endpoint, so nothing is
sent anywhere until the user names a server -- key OPTIONAL, empty
sends no Authorization header, the local-server shape), installed
via installAI, which keys the answer cache by model + the
endpoint's HOSTNAME (never userinfo: the string is written to the
cache file) so one model name pointed at two servers cannot cross
answers. kagi.go: KagiClient
(NewKagiClient(key, maxResults); BaseURL/HTTPClient/Now/Logf
exported seams -- BaseURL doubles as the production
preview.kagi.baseUrl override and REPLACES the whole default base
verbatim) -- coded against the Kagi OpenAPI spec
(https://kagi.redocly.app/_spec/openapi.yaml, fetched 2026-07-20):
POST {base}/search (default base = the spec's server URL
https://kagi.com/api/v1), JSON body {"query","limit"}, header
`Authorization: Bearer <key>` -- the earlier GET
/api/v1/search?q=&limit= + "Bot" header combo 404s, the /search
route exists only for POST (verified live: GET = 404, POST = 401
keyless) -- response data.search rows {url,title,snippet} (the
long-dead v0 flat data array with t==0 rows is still accepted on
parse); Search(ctx, q) -> (results, cachedBool, err) with an
exact-query
TTL cache (15min, 100 entries, oldest-inserted evicted; hits =
zero network + no token spend) and a client-side token bucket
(burst 3, refill 1/s; empty = "kagi: rate limited, retry shortly"
WITHOUT dialing); non-2xx = "kagi: HTTP <code>" + at most a
200-char parsed error message (spec errorEnvelope
error[].message, legacy "msg" fallback) -- never the raw body,
never the key -- plus ONE Logf line per failure quoting the Kagi
trace id (X-Kagi-Trace header else meta.trace, capped 64 bytes;
the id Kagi support asks for), wired by dispatch.go's New under
the "preview: " prefix.
aiclient.go: AIClient (NewAIClient(key, model, maxOutputTokens);
BaseURL/HTTPClient exported seams) -- the ONE AI client, speaking
OpenAI-compatible chat completions because that is what OpenAI,
Anthropic's compat endpoint, OpenRouter, Ollama, llama.cpp, LM
Studio and vLLM all implement: POST {base}/chat/completions
{"model","messages":[{role:user}],"max_tokens"}, BaseURL being the
API base INCLUDING the version segment (every SDK's base_url
convention), an EMPTY key sending no Authorization header at all;
Ask(ctx, prompt) -> (answer, resolvedModel, err) concatenating the
choices' message content, finish_reason "length" appending a
"[truncated by maxOutputTokens]" marker line, non-2xx = "ai: HTTP
<code>" + at most a 200-char parsed {"error":{"message"}} (a bare
string error is read too -- local servers send them), the key
confined to the header, never logged, never in errors. probe.go:
`ProbeProvider(ctx, ProbeParams{Provider kagi|ai, APIKey,
BaseURL, Model})` -> ProbeResult{OK, Message} -- the
config editor Test buttons' engine: bounded inputs (key 4096 /
base 2048 / model 256 bytes -- wire-abuse defense), ONE minimal
real request per call (kagi = a REAL limit-1 search, which spends
one API credit -- the cheapest honest test the API offers, the
button hint says so; ai = one ask capped at 16
output tokens, the smallest cap the strictest servers accept),
candidate values resolved exactly like the live wiring (ai:
base+model required, key optional), honest ok/error messages that
carry the HTTP status + capped provider message but never the key
or raw body. aicache.go: AICache -- the persistent AI answer LRU
on internal/history's atomic pattern (lazy one-shot Load: missing =
empty+nil, corrupt = empty + error, logged once via the Logf seam;
temp-file+rename 0600 writes, MkdirAll parent; in-memory updates
even when the write fails; "" path = memory-only): {"v":1,
"entries":[{k,model,prompt,answer,at}]} at Options.AICachePath, k =
sha256 hex of model+NUL+FULL prompt where the stored "model" is the
ENDPOINT-QUALIFIED "<configured model>@<baseUrl hostname>"
(installAI in aiprovider.go; the same model name pointed at a
different server can never serve that server's answers, and the
hostname alone keeps userinfo out of the cache file) (the stored prompt is capped
2KB, answer 32KB), Get(model,prompt) refreshes recency (At),
Put evicts past 128 entries by oldest At; hits emit Cached:true
with zero network. cache.go: bytes-bounded LRU of rich payloads
(16 MiB / 64 entries; key = path + mtime + size + provider kind);
hits skip the meta emission. text.go: IsBinary (NUL or >30% bad
bytes), ReadCapped (maxKB, ToValidUTF8-sanitized), LangHint (~35
extensions + Dockerfile/Makefile name matches -> highlight.js
names). image.go: Thumbnail -- extension gate
(png/jpg/jpeg/gif/webp/bmp), 32 MiB source + 40-megapixel
DecodeConfig gates, decode raced against ctx, x/image/draw
ApproxBiLinear downscale to maxEdge, JPEG q80 for JPEG sources else
PNG, base64 data URI. dir.go: ListCapped (dirs first,
case-insensitive, capped, entry.Info sizes, never recurses).
meta.go: MetaFor (humanized size, mtime, mode, kind guess, path +
extra rows). Wired by internal/app's preview.go: startPreview runs
unless config.Enabled(preview.enabled) is false (the pane is ON by
default since config v8; the file/dir/image/meta previews need
ZERO configuration -- keyless installs get them out of the box
while the web/AI strip buttons render disabled with a
configure-hint) and resolves each API key
ONCE -- config value, else the env var through the getenv seam
(KAGI_API_KEY / COMPETENT_SEARCH_AI_API_KEY), exactly the
resolution
GetPreviewConfig reports. Base URLs are CONFIG-ONLY (no env
fallback anywhere): there is no default AI endpoint, so nothing is
sent anywhere until preview.ai.baseUrl names a server. It also
passes <configDir>/aicache.json
(config.Dir() failure = one log line + memory-only cache); the
keys and base URLs flow only into preview.Options, never into logs
or payloads;
bound methods QueryPreview(target, gen) / GetPreviewConfig()
(enabled + kagiConfigured + aiConfigured -- the AI half is
baseUrl AND model, the key being optional -- + resultsWidth = the flag-off bar
width, Options.ResultsWidth wired from config window.width in
main.go with a DefaultWindowWidth fallback when unset; keys never
exposed) /
FetchWebPreview / FetchAIPreview (gen store + dispatcher FetchWeb/
FetchAI; nil dispatcher = no-op, so the frontend's Ctrl+K / Ctrl+I
strip is the ONLY call path and nothing automatic ever dials) /
`TestPreviewProvider(PreviewProviderTest{provider, apiKey,
baseUrl, model}) preview.ProbeResult` (testpreview.go: the config
editor Test buttons -- CANDIDATE possibly-unsaved values, empty
fields resolved through the SAME env fallbacks the live dispatcher
uses, then preview.ProbeProvider under a 15s hard timeout on the
bound method's own goroutine) / `OpenExternalURL(raw) error`
(openurl.go: the editor's clickable doc links -- open_url-grade
validation (http/https + host), then openTarget directly --
deliberately NOT Open(), which would hide the bar and record
frecency, both wrong for a doc link clicked mid-edit; the webview
NEVER navigates);
emissions
ride the "preview:result" event behind the previewGen atomic gate
(the QueryPlugins pattern); Shutdown cancels the dispatcher's
parent ctx. previewsize.go `PreviewWindowSize()` (translucent.go
pattern: fresh config.Load, any error = disabled) tells main.go the
window size BEFORE wails.Run; an explicit preview.enabled=false
opt-out or any config error = the configured base size
(window.width/height, defaults 780x550 -- the WindowSize read),
the default-ON pane = preview.windowWidth/Height (defaults
1100x700 -- a modest step up from the 780x550 bar, chosen when the
pane turned on by default), threaded into
Options.WindowWidth/WindowHeight for the positioning math
(App.windowSize()).
