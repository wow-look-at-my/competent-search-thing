# color-http -- color preview plugin (http / Go)

Shows a hex color typed into the search bar as a swatch-style result:
canonical `#rrggbb` title, the row accent set to the color itself,
RGB and HSL fields, and a copy-to-clipboard action.

This example demonstrates the HTTP transport: the searchbar POSTs one
JSON request per query to the manifest's `http.url` and renders the
JSON response. `colorhttp/` implements the endpoint against the
documented wire format only (it imports nothing from the app);
`server/` is a thin binary around it.

## Run the server

    go run ./examples/plugins/color-http/server

It listens on `127.0.0.1:8765`, matching manifest.json; change with
`-addr` (and update the manifest URL to match).

## Install the manifest

Copy `manifest.json` into `<config dir>/plugins/color/` (see the calc
example's README for the per-OS config directories), then restart the
app or run `!reload`.

## Use

    #ff8800
    #f80
    !color 336699

## Protocol notes

The handler accepts POST on any path. A non-POST method gets 405 and
a malformed JSON body gets 400; the searchbar treats non-2xx answers
as plugin errors and logs them. A query that is not a color returns
`{"v":1,"results":[]}`, which renders nothing.
