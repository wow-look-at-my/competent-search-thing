# ps -- running-processes plugin (command / python3, bang-targeted)

Filters the running applications the searchbar already knows about:
`!ps fire` lists running apps whose name or window title contains
"fire"; `!ps ` (note the space) lists them all, capped at 15. Enter
copies the PID.

Demonstrates two manifest features:

- **Bang-only targeting**: the manifest has NO `trigger` key, so the
  plugin is unreachable except through its bang (`!ps ...`).
- **The `context` declaration**: `"context": ["running"]` requests the
  running-app snapshot in every request; context parts a plugin does
  not declare (focused window, installed apps) are never sent to it.

Install like the calc example: copy this directory into
`<config dir>/plugins/ps/` (python3 required; see calc's README for
the per-OS directories).
