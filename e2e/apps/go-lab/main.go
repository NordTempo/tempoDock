// go-lab is a small HTTP app whose failure modes are switched on with
// variables, so one image covers the failure-path scenarios:
//
//	VERSION        text returned by GET / (default "v1")
//	FAIL_HEALTH=1  /healthz returns 500
//	EXIT_AT_START  exit with this code before listening
//	CRASH_AFTER    exit 1 after this many seconds (Go duration, e.g. 20s)
//	ALLOC_MB       allocate and touch this much memory at start
package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	if code := os.Getenv("EXIT_AT_START"); code != "" {
		n, _ := strconv.Atoi(code)
		log.Printf("exiting at start with %d", n)
		os.Exit(n)
	}
	if mb, _ := strconv.Atoi(os.Getenv("ALLOC_MB")); mb > 0 {
		buf := make([]byte, mb<<20)
		for i := range buf {
			buf[i] = 1
		}
		log.Printf("allocated %d MB", mb)
		defer fmt.Println(len(buf))
	}
	if d, err := time.ParseDuration(os.Getenv("CRASH_AFTER")); err == nil {
		go func() {
			time.Sleep(d)
			log.Printf("crashing after %s", d)
			os.Exit(1)
		}()
	}
	version := os.Getenv("VERSION")
	if version == "" {
		version = "v1"
	}
	host, _ := os.Hostname()
	http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "go-lab %s %s\n", version, host)
	})
	http.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if os.Getenv("FAIL_HEALTH") == "1" {
			http.Error(w, "unhealthy", http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "ok")
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("listening on %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
