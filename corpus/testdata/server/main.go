// Command server hosts a corpus description, so vertrag can be pointed at a
// conforming API without a real project being available.
//
//	go run ./corpus/testdata/server linked 8080 [fault...]
//
// It exists for working by hand — the automated conformance tests build the
// same server in-process. Faults are named on the command line, and `stateful`
// makes it mint identifiers rather than echo the documented one, which is what
// a sequencing run needs in order to prove anything.
package main

import (
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/antimatter-studios/vertrag/corpus"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "usage: server <description> <port> [stateful] [fault...]\n\ndescriptions: %s\n\nfaults:\n",
			strings.Join(corpus.Names(), ", "))
		for _, fault := range corpus.Faults() {
			fmt.Fprintf(os.Stderr, "  %s\n", fault)
		}
		os.Exit(2)
	}

	name, port := os.Args[1], os.Args[2]

	stateful := false
	var faults []corpus.Fault
	for _, argument := range os.Args[3:] {
		if argument == "stateful" {
			stateful = true
			continue
		}
		faults = append(faults, corpus.Fault(argument))
	}

	server, err := corpus.NewNamed(name, faults...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if stateful {
		server = server.Stateful()
	}

	fmt.Fprintf(os.Stderr, "hosting %s on http://127.0.0.1:%s\n", name, port)
	if err := http.ListenAndServe("127.0.0.1:"+port, server.Handler()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
