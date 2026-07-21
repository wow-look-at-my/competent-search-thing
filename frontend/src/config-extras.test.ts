// The config editor's provider extras: linkified descriptions whose
// clicks route through Go's OpenExternalURL (the webview never
// navigates), and the per-provider Test buttons probing the CANDIDATE
// working-copy values -- unsaved edits included -- through
// TestPreviewProvider. Runs in its own module graph (vitest isolates
// test files), so initConfig wires fresh here.

import { beforeAll, describe, expect, it } from "vitest";
import { initConfig, linkSegments, providerTestRequest } from "./config";

const schema = {
  properties: {
    preview: {
      type: "object",
      description: "The preview pane.",
      properties: {
        kagi: {
          type: "object",
          properties: {
            apiKey: {
              type: "string",
              description:
                "SECRET: the Kagi token -- get one at https://kagi.com/api/keys . Keep it private.",
            },
            baseUrl: { type: "string", description: "custom base" },
          },
        },
        custom: {
          type: "object",
          properties: {
            apiKey: { type: "string", description: "SECRET: optional." },
            baseUrl: { type: "string", description: "the endpoint" },
            model: { type: "string", description: "the model" },
          },
        },
      },
    },
  },
};

const docJson = JSON.stringify({
  preview: {
    kagi: { apiKey: "saved-key", baseUrl: "" },
    custom: { apiKey: "", baseUrl: "http://localhost:1234", model: "llama3" },
  },
});

const testedRequests: PreviewProviderTest[] = [];
const openedUrls: string[] = [];
let probeResult: PreviewProbeResult = { ok: true, message: "ok: probe" };

function fakeEnv(): { app: WailsAppBindings; fire: (name: string) => void } {
  const events = new Map<string, (...data: unknown[]) => void>();
  const app = {
    GetConfigSchema: () => Promise.resolve(JSON.stringify(schema)),
    GetConfigForEdit: () =>
      Promise.resolve({ configJson: docJson, path: "/tmp/config.json" }),
    SaveConfig: () => Promise.resolve({ ok: true, applied: [], pending: [] }),
    OpenConfigFile: () => Promise.resolve(),
    GetPreviewConfig: () =>
      Promise.resolve({
        enabled: false,
        kagiConfigured: false,
        aiProvider: "openai",
        aiConfigured: false,
        resultsWidth: 680,
      }),
    TestPreviewProvider: (req: PreviewProviderTest) => {
      testedRequests.push(req);
      return Promise.resolve(probeResult);
    },
    OpenExternalURL: (url: string) => {
      openedUrls.push(url);
      return Promise.resolve();
    },
  } as unknown as WailsAppBindings;
  const rt = {
    EventsOn: (name: string, cb: (...data: unknown[]) => void) => {
      events.set(name, cb);
      return () => {};
    },
    EventsOff: () => {},
  } as unknown as WailsRuntime;
  initConfig(app, rt);
  return {
    app,
    fire: (name: string) => {
      const cb = events.get(name);
      if (cb === undefined) {
        throw new Error("no handler for " + name);
      }
      cb();
    },
  };
}

function tick(): Promise<void> {
  return new Promise((resolve) => {
    setTimeout(resolve, 0);
  });
}

describe("linkSegments", () => {
  it("splits plain text and URLs, trimming sentence punctuation", () => {
    expect(
      linkSegments("get one at https://kagi.com/api/keys . Keep it private."),
    ).toEqual([
      { text: "get one at ", isUrl: false },
      { text: "https://kagi.com/api/keys", isUrl: true },
      { text: " . Keep it private.", isUrl: false },
    ]);
  });

  it("handles URL-free text, multiple URLs, and trailing URLs", () => {
    expect(linkSegments("no links here")).toEqual([
      { text: "no links here", isUrl: false },
    ]);
    const segs = linkSegments(
      "a https://a.example/x, then b (https://b.example/y)",
    );
    expect(segs.filter((s) => s.isUrl).map((s) => s.text)).toEqual([
      "https://a.example/x",
      "https://b.example/y",
    ]);
    expect(linkSegments("end https://c.example")).toEqual([
      { text: "end ", isUrl: false },
      { text: "https://c.example", isUrl: true },
    ]);
  });
});

describe("providerTestRequest", () => {
  const from = {
    preview: {
      kagi: { apiKey: "kk", baseUrl: "kb" },
      openai: { apiKey: "ok", baseUrl: "ob", model: "om" },
      anthropic: { apiKey: "ak", baseUrl: "ab", model: "am" },
      custom: { apiKey: "ck", baseUrl: "cb", model: "cm" },
    },
  };

  it("maps each provider section's candidate fields", () => {
    expect(providerTestRequest("preview.kagi", from)).toEqual({
      provider: "kagi",
      apiKey: "kk",
      baseUrl: "kb",
      model: "",
    });
    expect(providerTestRequest("preview.openai", from)).toEqual({
      provider: "openai",
      apiKey: "ok",
      baseUrl: "ob",
      model: "om",
    });
    expect(providerTestRequest("preview.anthropic", from)).toEqual({
      provider: "anthropic",
      apiKey: "ak",
      baseUrl: "ab",
      model: "am",
    });
    expect(providerTestRequest("preview.custom", from)).toEqual({
      provider: "custom",
      apiKey: "ck",
      baseUrl: "cb",
      model: "cm",
    });
  });

  it("answers null for sections without a Test button", () => {
    expect(providerTestRequest("preview", from)).toBeNull();
    expect(providerTestRequest("search.frecency", from)).toBeNull();
    expect(providerTestRequest("preview.kagi.apiKey", from)).toBeNull();
  });

  it("degrades missing values to empty strings", () => {
    expect(providerTestRequest("preview.anthropic", {})).toEqual({
      provider: "anthropic",
      apiKey: "",
      baseUrl: "",
      model: "",
    });
  });
});

describe("editor provider extras (DOM)", () => {
  let fire: (name: string) => void;

  beforeAll(async () => {
    ({ fire } = fakeEnv());
    fire("config:open");
    await tick();
  });

  it("renders description URLs as clickable links that route through Go", () => {
    const link = document
      .getElementById("config-sec-preview.kagi")
      ?.querySelector<HTMLAnchorElement>(".config-link");
    expect(link).not.toBeNull();
    expect(link?.textContent).toBe("https://kagi.com/api/keys");
    expect(link?.getAttribute("href")).toBe("https://kagi.com/api/keys");

    const ev = new MouseEvent("click", { bubbles: true, cancelable: true });
    const notPrevented = link?.dispatchEvent(ev);
    expect(notPrevented).toBe(false); // preventDefault: no webview navigation
    expect(openedUrls).toEqual(["https://kagi.com/api/keys"]);
  });

  it("probes the CANDIDATE working copy on Test, unsaved edits included", async () => {
    // Edit the key WITHOUT saving, then hit Test: the probe must
    // carry the edited value.
    const input = document.getElementById(
      "cfg-preview-kagi-apiKey",
    ) as HTMLInputElement;
    expect(input).not.toBeNull();
    input.value = "unsaved-key";
    input.dispatchEvent(new Event("input", { bubbles: true }));

    const btn = document.getElementById(
      "cfg-test-preview-kagi",
    ) as HTMLButtonElement;
    expect(btn).not.toBeNull();
    btn.click();
    await tick();

    expect(testedRequests).toEqual([
      { provider: "kagi", apiKey: "unsaved-key", baseUrl: "", model: "" },
    ]);
    const result = document
      .getElementById("config-sec-preview.kagi")
      ?.querySelector<HTMLSpanElement>(".config-test-result");
    expect(result?.hidden).toBe(false);
    expect(result?.textContent).toBe("ok: probe");
    expect(result?.classList.contains("config-test-ok")).toBe(true);
    expect(btn.disabled).toBe(false);
  });

  it("renders a failed probe with the error class and the honest message", async () => {
    probeResult = { ok: false, message: "kagi: HTTP 401: Invalid API Key" };
    const btn = document.getElementById(
      "cfg-test-preview-custom",
    ) as HTMLButtonElement;
    expect(btn).not.toBeNull();
    btn.click();
    await tick();

    expect(testedRequests.at(-1)).toEqual({
      provider: "custom",
      apiKey: "",
      baseUrl: "http://localhost:1234",
      model: "llama3",
    });
    const result = document
      .getElementById("config-sec-preview.custom")
      ?.querySelector<HTMLSpanElement>(".config-test-result");
    expect(result?.textContent).toBe("kagi: HTTP 401: Invalid API Key");
    expect(result?.classList.contains("config-test-err")).toBe(true);
  });

  it("mentions the Kagi credit cost in the button hint", () => {
    const hint = document
      .getElementById("config-sec-preview.kagi")
      ?.querySelector<HTMLElement>(".config-test-row .config-help");
    expect(hint?.textContent).toContain("1 Kagi API credit");
  });
});
