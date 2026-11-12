//go:build windows

package main

import (
	"fmt"
	"net/http"
)

// serve offers a folder of saved clips over HTTP on this computer only, so
// that a web browser can be used as a second, independent player to check
// them with.
func serve() {
	dir, port := text("dir", "."), option("port", 8765)
	address := fmt.Sprintf("127.0.0.1:%d", port)
	fmt.Println("serving", dir, "on http://"+address)
	if err := http.ListenAndServe(address, http.FileServer(http.Dir(dir))); err != nil {
		fail(err)
	}
}
