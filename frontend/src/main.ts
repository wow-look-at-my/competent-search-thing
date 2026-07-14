// Minimal wiring for the scaffold phase: focus the input, wait for the
// Wails bindings global (window.go) to appear, then hook the input up
// to App.Search and the Escape key up to App.Hide. The real UI
// (keyboard navigation, open/reveal actions, richer rendering) lands in
// a later phase.

const inputEl = document.getElementById("query") as HTMLInputElement;
const resultsEl = document.getElementById("results") as HTMLDivElement;
const statusEl = document.getElementById("status") as HTMLDivElement;

function bindings(): WailsAppBindings | null {
  return window.go?.app.App ?? null;
}

function render(items: WailsSearchResult[]): void {
  resultsEl.replaceChildren();
  for (const item of items) {
    const row = document.createElement("div");
    row.className = "result";
    row.textContent = item.name + "  --  " + item.path;
    resultsEl.appendChild(row);
  }
  statusEl.textContent = items.length + " result(s)";
}

async function runSearch(app: WailsAppBindings): Promise<void> {
  try {
    const items = await app.Search(inputEl.value);
    render(items);
  } catch (err) {
    statusEl.textContent = "search error: " + String(err);
  }
}

function wire(app: WailsAppBindings): void {
  inputEl.addEventListener("input", () => {
    void runSearch(app);
  });
  document.addEventListener("keydown", (ev: KeyboardEvent) => {
    if (ev.key === "Escape") {
      void app.Hide();
    }
  });
  statusEl.textContent = "ready";
}

// window.go is injected by the Wails runtime shortly after page load;
// poll until it exists, then wire everything up.
function waitForBindings(): void {
  const app = bindings();
  if (app !== null) {
    wire(app);
    return;
  }
  window.setTimeout(waitForBindings, 50);
}

inputEl.focus();
waitForBindings();

export {};
