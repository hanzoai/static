package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"

	"github.com/hanzoai/static"
)

func main() {
	port := flag.Int("port", 3000, "listen port")
	root := flag.String("root", "/public", "root directory")
	spa := flag.Bool("spa", false, "SPA mode (serve index.html for 404s)")
	flag.Parse()

	cfg := &static.Config{
		Root:       *root,
		IndexFiles: []string{"index.html"},
		SPAMode:    *spa,
		SPAIndex:   "index.html",
	}

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", 404)
	})
	handler, err := static.New(context.Background(), notFound, cfg, "static")
	if err != nil {
		log.Fatalf("static: %v", err)
	}

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("static: serving %s on %s (spa=%v)", *root, addr, *spa)
	log.Fatal(http.ListenAndServe(addr, handler))
}
