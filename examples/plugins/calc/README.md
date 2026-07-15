# calc -- calculator plugin (command / python3)

Evaluates plain arithmetic typed into the search bar and shows the
value as a virtual result: Hex/Binary fields for integers and a
copy-to-clipboard action.

Requires `python3` on PATH. On Windows, where the interpreter is
usually named `python`, change `command.argv` in manifest.json
accordingly.

## Install

Copy this directory into the app's plugin directory as `calc/`:

| OS      | plugin directory                                                 |
|---------|------------------------------------------------------------------|
| Linux   | `~/.config/competent-search-thing/plugins/`                      |
| macOS   | `~/Library/Application Support/competent-search-thing/plugins/`  |
| Windows | `%AppData%\competent-search-thing\plugins\`                      |

If `COMPETENT_SEARCH_CONFIG_DIR` is set, it replaces the per-OS config
directory; the plugin directory is always `<config dir>/plugins/`.

Restart the app (or run `!reload`) to pick it up.

## Use

    =2+2
    !calc 2+2
    !c 2+2

Supported: `+ - * / // % **` with parentheses and unary `+`/`-` over
int/float literals. Anything else simply produces no results.
