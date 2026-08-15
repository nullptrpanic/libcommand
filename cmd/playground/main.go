package main

import (
	"flag"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"time"
)

const (
	defaultAddress              = "127.0.0.1:8080"
	contentSecurityPolicy       = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	workerContentSecurityPolicy = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval' 'unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
)

func main() {
	address := flag.String("addr", defaultAddress, "HTTP listen address")
	flag.Parse()

	assets, err := playgroundAssets()
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              *address,
		Handler:           newHandler(assets),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("libcommand playground: http://%s", *address)
	log.Fatal(server.ListenAndServe())
}

func newHandler(assets fs.FS) http.Handler {
	files := http.FileServer(http.FS(assets))
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		policy := contentSecurityPolicy
		if request.URL.Path == "/worker.js" {
			policy = workerContentSecurityPolicy
		}
		response.Header().Set("Content-Security-Policy", policy)
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("X-Content-Type-Options", "nosniff")
		if strings.HasSuffix(request.URL.Path, ".wasm") {
			response.Header().Set("Content-Type", "application/wasm")
		}
		files.ServeHTTP(response, request)
	})
}
