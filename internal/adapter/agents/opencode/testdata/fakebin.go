// fakebin is a stand-in for `opencode serve` used by the manager tests in
// CI environments where the real binary is not installed. It listens on the
// port passed via --port and replies 200 OK on /global/health.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 3 || os.Args[1] != "serve" {
		fmt.Fprintln(os.Stderr, "usage: fakebin serve --port N")
		os.Exit(2)
	}
	port, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintf(os.Stderr, "invalid port: %v\n", err)
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/global/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"test"}`))
	})
	server := &http.Server{Addr: ":" + strconv.Itoa(port), Handler: mux}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "fakebin: %v\n", err)
		os.Exit(1)
	}
}
