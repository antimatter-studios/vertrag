// Package graphql reads a GraphQL schema into the small in-memory form the
// rest of vertrag needs in order to test one.
//
// Nothing about GraphQL maps onto the shape an OpenAPI description has: there
// is one endpoint, one method, and the entire surface of the API lives inside
// the request body. What corresponds to "the set of things that can be called"
// is the type system — the fields of the query and mutation root types, and the
// types their arguments and results are built out of — so that is what this
// reads, and everything a schema says that testing cannot act on is reported
// rather than dropped.
//
// Two input forms are read, because two are what people actually have. SDL is
// what is checked into a repository. An introspection result is what a running
// server hands you, and for a schema stitched together at run time from several
// services it is often the only complete form that exists anywhere. They carry
// the same information in different shapes, and both land in the same types
// here so that nothing downstream has to know which one it came from.
//
// Neither form is read with a dependency. vertrag ships a static binary and
// treats every dependency as a liability; SDL is lexed and parsed by hand (see
// lexer.go), and an introspection result is JSON, which the standard library
// already reads.
package graphql

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Kind is what a named type is.
type Kind int

const (
	KindScalar Kind = iota
	KindObject
	KindInterface
	KindUnion
	KindEnum
	KindInputObject
)

// String names a kind the way a message about it should read.
func (k Kind) String() string {
	switch k {
	case KindScalar:
		return "scalar"
	case KindObject:
		return "object"
	case KindInterface:
		return "interface"
	case KindUnion:
		return "union"
	case KindEnum:
		return "enum"
	case KindInputObject:
		return "input object"
	default:
		return "unknown"
	}
}

// Schema is a GraphQL schema reduced to what testing needs.
type Schema struct {
	// Root operation type names, "" when the schema declares none.
	QueryType, MutationType, SubscriptionType string
	Types                                     map[string]*Type

	// Description is the schema's own description, which the specification has
	// allowed since 2018 and introspection exposes as `__schema { description }`.
	// It is recorded rather than dropped; nothing here acts on it.
	Description string
}

// Type is one named type.
type Type struct {
	Name        string
	Kind        Kind
	Description string
	Fields      []Field  // Object, Interface, InputObject
	EnumValues  []string // Enum
	Possible    []string // Union members, and the objects implementing an Interface
	Interfaces  []string // Object: the interfaces it implements

	// DeprecatedEnumValues names the members of EnumValues carrying
	// `@deprecated`. EnumValues still lists them, because a deprecated value is
	// still a legal one to send — this is here so a generator can prefer the
	// others rather than have to be told about deprecation in a warning it
	// cannot act on.
	DeprecatedEnumValues []string

	// OneOf marks an input object whose value must set exactly one of its
	// fields. It comes from the `@oneOf` directive, or from introspection's
	// `isOneOf`. It matters to generation and to nothing else: an ordinary
	// input object filled in field by field is what a @oneOf input rejects.
	OneOf bool

	// SpecifiedBy is the URL a custom scalar's `@specifiedBy` directive points
	// at. Recorded because a custom scalar is otherwise opaque, and the URL is
	// the only thing the schema says about what its values look like.
	SpecifiedBy string

	// EnumValueDescriptions is the documentation written on individual enum
	// values, keyed by value. It is a map beside EnumValues rather than a
	// richer element type because EnumValues is part of the contract with the
	// rest of vertrag and stays a plain list of names.
	EnumValueDescriptions map[string]string
}

// Field is a field of an object/interface, or an input field of an input object.
type Field struct {
	Name        string
	Description string
	Type        TypeRef
	Args        []Argument
	Deprecated  bool

	// DeprecationReason is what `@deprecated(reason: ...)` said. Kept so the
	// reason reaches a report rather than being reduced to a boolean.
	DeprecationReason string

	// Default and HasDefault carry an INPUT-OBJECT field's default value.
	//
	// The same default is also present as a single entry in Args, named after
	// the field itself, because Argument is documented as covering "an
	// input-object field's default" and Args is the only place on a Field an
	// Argument can sit. Both are filled in, deliberately: a consumer that
	// reaches for either finds the same value, and neither reading is wrong.
	Default    any
	HasDefault bool
}

// DefaultValue reports the default an input-object field carries, from
// whichever of the two representations is populated.
func (f Field) DefaultValue() (any, bool) {
	if f.HasDefault {
		return f.Default, true
	}
	for _, arg := range f.Args {
		if arg.Name == f.Name && arg.HasDefault {
			return arg.Default, true
		}
	}
	return nil, false
}

// Argument is a field argument or an input-object field's default.
type Argument struct {
	Name        string
	Description string
	Type        TypeRef
	Default     any // nil when none was written
	HasDefault  bool

	// Deprecated records `@deprecated` on an argument or input field, which the
	// specification allows on any argument that is not required. Same reasoning
	// as Field.Deprecated: acted on rather than warned about.
	Deprecated        bool
	DeprecationReason string
}

// TypeRef is a possibly-wrapped type reference: `[Foo!]!`.
// Exactly one of Named and List is set.
type TypeRef struct {
	Named   string
	List    *TypeRef
	NonNull bool
}

// String renders a reference the way the schema spells it, for messages.
func (r TypeRef) String() string {
	var text string
	switch {
	case r.List != nil:
		text = "[" + r.List.String() + "]"
	case r.Named != "":
		text = r.Named
	default:
		text = "?"
	}
	if r.NonNull {
		text += "!"
	}
	return text
}

// Resolve follows a TypeRef to the named type it ultimately refers to,
// reporting how many list wrappers were passed through.
//
// The wrapper count is returned rather than discarded because it is the whole
// difference between a value and a list of them: `[User]` and `User` resolve to
// the same type and demand entirely different request bodies.
func (s *Schema) Resolve(ref TypeRef) (*Type, int, bool) {
	lists := 0
	for ref.Named == "" && ref.List != nil {
		lists++
		ref = *ref.List
	}
	if ref.Named == "" || s == nil {
		return nil, lists, false
	}
	named, ok := s.Types[ref.Named]
	if !ok {
		return nil, lists, false
	}
	return named, lists, true
}

// Detection patterns.
//
// An introspection result is recognised by the one key the standard
// introspection query is built around, which no other description format
// contains.
//
// SDL is recognised by a definition keyword followed by a name — not by the
// keyword alone. `type` on its own appears on nearly every line of an OpenAPI
// document, and matching it would make every OpenAPI file a GraphQL schema;
// what OpenAPI never contains is `type Name {`, because in YAML and JSON the
// key is always followed by a colon.
var (
	introspectionPattern = regexp.MustCompile(`"__schema"\s*:`)

	sdlPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?m)^\s*(?:extend\s+)?(?:type|interface)\s+[_A-Za-z][_0-9A-Za-z]*\s*(?:implements\b[^{]*)?(?:@[^{]*)?\{`),
		regexp.MustCompile(`(?m)^\s*(?:extend\s+)?(?:input|enum)\s+[_A-Za-z][_0-9A-Za-z]*\s*(?:@[^{]*)?\{`),
		regexp.MustCompile(`(?m)^\s*(?:extend\s+)?union\s+[_A-Za-z][_0-9A-Za-z]*\s*(?:@[^=]*)?=`),
		regexp.MustCompile(`(?m)^\s*(?:extend\s+)?scalar\s+[_A-Za-z][_0-9A-Za-z]*`),
		regexp.MustCompile(`(?m)^\s*(?:extend\s+)?schema\s*(?:@[^{]*)?\{`),
	}
)

// Detect reports whether the source looks like a GraphQL schema.
func Detect(source []byte) bool {
	if introspectionPattern.Match(source) {
		return true
	}
	for _, pattern := range sdlPatterns {
		if pattern.Match(source) {
			return true
		}
	}
	return false
}

// Parse reads a schema from SDL or from an introspection result.
//
// The three returns say three different things and the middle one is the point
// of the package. An error means the document could not be read at all. The
// warnings are everything the document says that this does not act on — an
// unmodelled directive, a subscription root, a field whose type is never
// defined. None of those stops a schema being useful, and every one of them
// would otherwise become a silent hole in a test run that looks like a pass.
func Parse(source []byte) (*Schema, []string, error) {
	if looksLikeJSON(source) {
		return parseIntrospection(source)
	}
	return parseSDL(source)
}

// looksLikeJSON decides which reader gets the document.
//
// The first meaningful character is enough: an introspection result is a JSON
// object, and a schema document that begins with `{` is an anonymous operation
// rather than a schema, which neither reader accepts anyway.
func looksLikeJSON(source []byte) bool {
	trimmed := strings.TrimLeft(strings.TrimPrefix(string(source), "\ufeff"), " \t\r\n")
	return strings.HasPrefix(trimmed, "{")
}

// builtInScalars are the five scalars every schema has whether it says so or
// not. They are added when missing so that the cross-check below does not
// report `String` as an undefined type on every schema ever written.
var builtInScalars = []string{"Int", "Float", "String", "Boolean", "ID"}

// conventionalRoots is the specification's answer to a schema with no `schema`
// block: the root types are the ones with these names, if such types exist.
var conventionalRoots = []struct {
	operation string
	name      string
	field     func(*Schema) *string
}{
	{"query", "Query", func(s *Schema) *string { return &s.QueryType }},
	{"mutation", "Mutation", func(s *Schema) *string { return &s.MutationType }},
	{"subscription", "Subscription", func(s *Schema) *string { return &s.SubscriptionType }},
}

// finalise is the part of reading a schema that is the same whichever form it
// arrived in: fill in what the document left implicit, then say what is wrong
// with the result.
func finalise(s *Schema, w *warnings) {
	if s.Types == nil {
		s.Types = map[string]*Type{}
	}
	for _, name := range builtInScalars {
		if _, defined := s.Types[name]; !defined {
			s.Types[name] = &Type{
				Name:        name,
				Kind:        KindScalar,
				Description: "A built-in GraphQL scalar.",
			}
		}
	}

	applyRootDefaults(s, w)
	s.crossCheck(w)
}

// applyRootDefaults fills in the roots a schema left implicit and reports what
// is wrong with the ones it named.
func applyRootDefaults(s *Schema, w *warnings) {
	for _, root := range conventionalRoots {
		target := root.field(s)

		if *target == "" {
			// The specification's default. A schema with no `schema` block is
			// the common case by a wide margin, and reading one as having no
			// operations at all would silently produce an empty test run.
			if named, ok := s.Types[root.name]; ok && named.Kind == KindObject {
				*target = root.name
			}
			continue
		}

		named, ok := s.Types[*target]
		switch {
		case !ok:
			w.add("the schema names %s as its %s root type, but no such type is defined: "+
				"nothing can be tested through it", *target, root.operation)
		case named.Kind != KindObject:
			w.add("the schema names %s as its %s root type, but %s is %s an %s rather than an object type",
				*target, root.operation, *target, article(named.Kind.String()), named.Kind)
		}
	}

	if s.QueryType == "" {
		w.add("this schema has no query root type, so nothing can be read from it: " +
			"GraphQL requires one, and a schema with no `schema { query: ... }` block gets it " +
			"from a type named `Query`")
	}

	// Named rather than dropped, because an untested subscription is invisible
	// otherwise: the run would simply contain nothing for it and look complete.
	if s.SubscriptionType != "" {
		w.add("the schema declares %s as its subscription root type; subscriptions run over a "+
			"persistent transport rather than a plain HTTP request, so vertrag does not test them",
			s.SubscriptionType)
	}
}

// crossCheck reports every reference to a type the schema never defines.
//
// This is the check worth having. A field whose type is missing is not a
// cosmetic problem: it is usually a schema assembled from several files with
// one of them left out, and the failure mode is a run that tests whatever
// remains, passes, and says nothing about the half that was never there. So
// each dangling reference is named individually, with the field it was written
// on, because "some type is missing" is not something anyone can act on.
//
// The types are walked in name order so that the warnings come out the same way
// every time: they are compared in tests and read in reports, and a map's
// iteration order would make both useless.
func (s *Schema) crossCheck(w *warnings) {
	for _, name := range sortedTypeNames(s.Types) {
		named := s.Types[name]

		for _, field := range named.Fields {
			where := name + "." + field.Name
			s.checkRef(w, "field "+where, field.Type)
			for _, arg := range field.Args {
				// An input field's default is carried as an argument named
				// after the field itself; it refers to the same type, which
				// has already been checked as the field's own.
				if named.Kind == KindInputObject && arg.Name == field.Name {
					continue
				}
				s.checkRef(w, fmt.Sprintf("argument %s(%s:)", where, arg.Name), arg.Type)
			}
		}

		for _, member := range named.Possible {
			if _, ok := s.Types[member]; !ok {
				what := "the union %s lists %s as a member, but no such type is defined"
				if named.Kind == KindInterface {
					what = "the interface %s is implemented by %s, but no such type is defined"
				}
				w.add(what, name, member)
			}
		}

		for _, iface := range named.Interfaces {
			if _, ok := s.Types[iface]; !ok {
				w.add("%s implements the interface %s, but no such type is defined", name, iface)
			}
		}
	}
}

func (s *Schema) checkRef(w *warnings, where string, ref TypeRef) {
	named := ref
	for named.Named == "" && named.List != nil {
		named = *named.List
	}
	if named.Named == "" {
		w.add("%s has no type", where)
		return
	}
	if _, ok := s.Types[named.Named]; !ok {
		w.add("%s refers to the type %s, which this schema does not define", where, named.Named)
	}
}

func sortedTypeNames(types map[string]*Type) []string {
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func article(word string) string {
	if strings.IndexByte("aeiou", word[0]) >= 0 {
		return "an"
	}
	return "a"
}

// warnings collects what a document said that this package does not act on.
//
// Directive applications are counted rather than listed. A schema that puts
// `@auth` on four hundred fields would otherwise produce four hundred warnings
// saying the same thing, which is how a warning list stops being read at all —
// and the whole point of the list is that it gets read. One line per directive,
// with the count and the first place it appeared, says the same thing and
// survives being looked at.
type warnings struct {
	messages   []string
	directives map[string]*directiveUse
}

type directiveUse struct {
	count int
	first string
}

func (w *warnings) add(format string, args ...any) {
	w.messages = append(w.messages, fmt.Sprintf(format, args...))
}

// noteDirective records one application of a directive that is not modelled.
func (w *warnings) noteDirective(name, where string) {
	if w.directives == nil {
		w.directives = map[string]*directiveUse{}
	}
	use, seen := w.directives[name]
	if !seen {
		use = &directiveUse{first: where}
		w.directives[name] = use
	}
	use.count++
}

// list renders the warnings, directive summaries last and in name order.
func (w *warnings) list() []string {
	out := append([]string(nil), w.messages...)

	names := make([]string, 0, len(w.directives))
	for name := range w.directives {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		use := w.directives[name]
		places := "place"
		if use.count > 1 {
			places = "places"
		}
		out = append(out, fmt.Sprintf(
			"the directive @%s is applied in %d %s (first on %s) and vertrag does not model it, "+
				"so whatever it changes about this API is not tested",
			name, use.count, places, use.first))
	}
	return out
}
