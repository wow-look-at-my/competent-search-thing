// Command server runs the color-http example plugin endpoint,
// serving colorhttp.Handler on -addr. The default matches the URL in
// the example manifest.json.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/wow-look-at-my/competent-search-thing/examples/plugins/color-http/colorhttp"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8765", "listen address")
	flag.Parse()
	log.Printf("color-http example plugin: listening on http://%s/query", *addr)
	log.Fatal(http.ListenAndServe(*addr, colorhttp.Handler()))
}
