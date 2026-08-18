package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/antimatter-studios/vertrag/apidesc"
	"github.com/antimatter-studios/vertrag/compile"
	"github.com/antimatter-studios/vertrag/config"
)

// transactionsFor reads a description and derives the transactions to test.
//
// Two formats, two paths, one output type. OpenAPI becomes API Elements and
// goes through the compiler every format shares; a GraphQL schema goes to its
// own compiler, because it has no resources or methods for API Elements to
// hold — the reasoning is at the top of compile/graphql.go. Both produce
// compile.Result, so this is the only place in the command that knows there is
// more than one path at all.
//
// The notes it returns are what a GraphQL run withheld. They are returned
// rather than printed so that every diagnostic in the command still goes out
// from the one place that decides where diagnostics go.
func transactionsFor(source []byte, filename string, settings config.Config) (compile.Result, []string, error) {
	parsed, err := apidesc.Parse(source, filename)
	if err != nil {
		return compile.Result{}, nil, fmt.Errorf("parsing %s: %w", filename, err)
	}

	if parsed.MediaType != apidesc.MediaTypeGraphQL {
		return compile.Compile(parsed.MediaType, parsed.Elements, filename), nil, nil
	}

	result := compile.CompileGraphQL(parsed.Schema, graphqlOptions(settings), filename)

	// The reader's warnings become annotations, which is the channel every
	// other parser diagnostic already uses — so a directive nobody models and
	// an OpenAPI parse error are reported the same way and read the same way.
	// They come first because they are about the document, and the compiler's
	// are about what could be built from it.
	compiled := result.Result
	annotations := make([]compile.Annotation, 0, len(parsed.Warnings)+len(compiled.Annotations))
	for _, warning := range parsed.Warnings {
		annotations = append(annotations, compile.Annotation{
			Type: "warning", Component: "apiDescriptionParser", Message: warning,
		})
	}
	compiled.Annotations = append(annotations, compiled.Annotations...)
	return compiled, result.Notes(), nil
}

// graphqlPhaseNotes reports the probing phases that can do nothing with a
// GraphQL schema.
//
// Coverage and fuzz generate values from the JSON Schema a request body
// carries, and a GraphQL request body is a query document with no schema
// behind it — so both would run over nothing and report nothing found, which
// reads exactly like a server that handles everything correctly. The stateful
// phase is in the same position: it follows the links a description declares
// between operations, and a schema declares none.
//
// The useful version of all three for GraphQL is generating ARGUMENT values,
// which is a round of its own. Until then this says so, because a phase that
// was asked for and silently did nothing is the failure this repository keeps
// meeting.
func graphqlPhaseNotes(mediaType string, phases []string) []string {
	if mediaType != compile.MediaTypeGraphQL {
		return nil
	}

	var idle []string
	for _, phase := range phases {
		if phase != config.PhaseExamples {
			idle = append(idle, phase)
		}
	}
	if len(idle) == 0 {
		return nil
	}

	subject := "the " + strings.Join(idle, " and ") + " phase has"
	if len(idle) > 1 {
		subject = "the " + strings.Join(idle, " and ") + " phases have"
	}
	return []string{fmt.Sprintf(
		"%s nothing to work on in a GraphQL schema and will report nothing: generation draws "+
			"values from the JSON Schema a request body carries, and a GraphQL request body is a "+
			"query document with no schema behind it. Probing a GraphQL API means generating "+
			"argument values, which vertrag does not do yet", subject)}
}

func graphqlOptions(settings config.Config) compile.GraphQLOptions {
	return compile.GraphQLOptions{
		Path:      settings.GraphQL.Path,
		MaxDepth:  settings.GraphQL.MaxDepth,
		Mutations: settings.GraphQL.Mutations,
	}
}

// graphqlFlags are the GraphQL settings a single run can override.
//
// They are on `compile` as well as `run` so that the two agree about what
// exists: a `compile` that listed the mutations a `run` then declined to send
// would read as a bug in the run.
type graphqlFlags struct {
	mutations bool
	path      string
	maxDepth  int
}

func addGraphQLFlags(fs *flag.FlagSet, f *graphqlFlags) {
	fs.BoolVar(&f.mutations, "graphql-mutations", false,
		"also send the GraphQL schema's mutations, which change the server's state (default: queries only)")
	fs.StringVar(&f.path, "graphql-path", "",
		"path the GraphQL queries are POSTed to (default: /graphql)")
	fs.IntVar(&f.maxDepth, "graphql-max-depth", 0,
		"how deeply a generated selection set may nest (default: 4)")
}

// apply merges the flags into the settings, the same way as everywhere else:
// the file says what the project normally does, a flag says what this run
// should do instead.
//
// `--graphql-mutations` is one-way, like every other boolean flag here. There
// is deliberately no spelling of it that turns mutations OFF for one run,
// because the setting that matters is the one in the file: a project that has
// enabled mutations has decided its schema is safe to mutate, and a flag
// meaning "not this time" would only ever be typed by somebody who has already
// realised it is not.
func (f graphqlFlags) apply(settings *config.GraphQLSettings) {
	if f.mutations {
		settings.Mutations = true
	}
	if f.path != "" {
		settings.Path = f.path
	}
	if f.maxDepth > 0 {
		settings.MaxDepth = f.maxDepth
	}
}
