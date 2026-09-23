//go:build ignore

// Serve a directory over HTTP on a local address, so CI can run the install
// scripts against freshly built release files instead of GitHub.
//
//	go run .github/scripts/serve.go 127.0.0.1:8765 dist
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: serve ADDR DIR")
	}
	log.Fatal(http.ListenAndServe(os.Args[1], http.FileServer(http.Dir(os.Args[2]))))
}
