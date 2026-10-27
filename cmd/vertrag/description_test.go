package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const liveDescription = `{
  "openapi":"3.1.0", "info":{"title":"Live contract","version":"1"},
  "paths":{"/widgets/{id}":{"get":{
    "operationId":"getWidget",
    "parameters":[
      {"name":"id","in":"path","required":true,"schema":{"type":"integer"},
       "examples":{"found":{"value":1},"missing":{"value":999}}},
      {"name":"X-Trace","in":"header","schema":{"type":"string"},"example":"fixed"}
    ],
    "responses":{
      "200":{"description":"Found","content":{"application/json":{
        "schema":{"type":"object","required":["id"],"properties":{"id":{"type":"integer"}}},
        "examples":{"found":{"value":{"id":1}}}}}},
      "404":{"description":"Missing","content":{"application/json":{
        "examples":{"missing":{"value":{"error":"missing"}}}}}}
    }
  }}}
}`

// Compile, selection, requests and failure verdicts must agree for the live
// description and a file containing its exact bytes. The fixture is read-only,
// so each run starts from identical state without a hidden reset requirement.
func TestLiveDescriptionAndSavedBytesDriveTheSameCLI(t *testing.T) {
	binary := build(t)
	var mu sync.Mutex
	var requests []string
	wrong := false
	wrongShape := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/openapi.json" {
			if r.Header.Get("Authorization") != "" {
				t.Error("the API credential leaked to the description fetch")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, liveDescription)
			return
		}
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.RequestURI()+" "+r.Header.Get("X-Trace")+" "+r.Header.Get("Authorization"))
		fail := wrong
		badBody := wrongShape
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":"unexpected"}`)
		} else if r.URL.Path == "/widgets/999" {
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":"missing"}`)
		} else {
			if badBody {
				_, _ = io.WriteString(w, `{"id":"wrong"}`)
			} else {
				_, _ = io.WriteString(w, `{"id":1}`)
			}
		}
	}))
	defer server.Close()
	file := filepath.Join(t.TempDir(), "openapi.json")
	if err := os.WriteFile(file, []byte(liveDescription), 0600); err != nil {
		t.Fatal(err)
	}
	live := server.URL + "/openapi.json?revision=fixture"
	compiledFile, fileCode := runCommand(t, binary, "compile", file)
	compiledLive, liveCode := runCommand(t, binary, "compile", live)
	if fileCode != 0 || liveCode != 0 || compiledLive != compiledFile {
		t.Fatalf("compile differs: file (%d) %s\nlive (%d) %s", fileCode, compiledFile, liveCode, compiledLive)
	}
	for _, mode := range []string{"valid", "wrong status", "wrong shape"} {
		var observed [][]string
		for _, source := range []string{file, live} {
			mu.Lock()
			wrong, wrongShape, requests = mode == "wrong status", mode == "wrong shape", nil
			mu.Unlock()
			output, code := runCommand(t, binary, "run", source, server.URL,
				"--operation-id", "getWidget", "--phases", "examples", "--no-color",
				"--header", "Authorization: Bearer api-only")
			want := 0
			if mode != "valid" {
				want = 1
			}
			if code != want {
				t.Fatalf("run %s: exit %d, want %d\n%s", source, code, want, output)
			}
			mu.Lock()
			observed = append(observed, append([]string(nil), requests...))
			mu.Unlock()
		}
		if !reflect.DeepEqual(observed[0], observed[1]) || len(observed[0]) != 2 {
			t.Fatalf("selected HTTP requests differ: %+v", observed)
		}
		want := []string{"GET /widgets/1 fixed Bearer api-only", "GET /widgets/999 fixed Bearer api-only"}
		if !reflect.DeepEqual(observed[0], want) {
			t.Fatalf("description selected %+v, want %+v", observed[0], want)
		}
	}
	mu.Lock()
	requests = nil
	mu.Unlock()
	if output, code := runCommand(t, binary, "doctor", live, server.URL); code != 0 {
		t.Fatalf("doctor could not read a live description: %d\n%s", code, output)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 0 {
		t.Fatalf("doctor sent API requests: %v", requests)
	}
}

func TestRemoteDescriptionLoading(t *testing.T) {
	t.Run("redirect and TLS", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/redirect" {
				http.Redirect(w, r, "/openapi.json", http.StatusFound)
				return
			}
			_, _ = io.WriteString(w, liveDescription)
		}))
		defer server.Close()
		body, err := readDescriptionWithClient(server.URL+"/redirect", server.Client())
		if err != nil || string(body) != liveDescription {
			t.Fatalf("body=%q err=%v", body, err)
		}
	})
	for _, code := range []int{204, 404, 500} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
			}))
			defer server.Close()
			if body, err := readDescription(server.URL); err == nil || body != nil || !strings.Contains(err.Error(), fmt.Sprint(code)) {
				t.Fatalf("HTTP %d produced body=%q err=%v", code, body, err)
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()
		if body, err := readDescriptionWithClient(server.URL, &http.Client{Timeout: 20 * time.Millisecond}); err == nil || body != nil {
			t.Fatalf("timeout produced body=%q err=%v", body, err)
		}
	})
	t.Run("bounded body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.CopyN(w, strings.NewReader(strings.Repeat(" ", maxRemoteDescriptionBytes+1)), maxRemoteDescriptionBytes+1)
		}))
		defer server.Close()
		if body, err := readDescription(server.URL); err == nil || body != nil || !strings.Contains(err.Error(), "limit") {
			t.Fatalf("oversized response produced %d bytes / %v", len(body), err)
		}
	})
	t.Run("local paths are unchanged", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "contract % spaces.json")
		if err := os.WriteFile(file, []byte(liveDescription), 0600); err != nil {
			t.Fatal(err)
		}
		body, err := readDescription(file)
		if err != nil || !bytes.Equal(body, []byte(liveDescription)) {
			t.Fatalf("local source changed: %v", err)
		}
		if _, err := readDescription(file + ".missing"); !os.IsNotExist(err) {
			t.Fatalf("local file error changed: %v", err)
		}
	})
}
