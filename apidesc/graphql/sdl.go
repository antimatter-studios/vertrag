package graphql

import "fmt"

// parseSDL reads a schema written in the schema definition language.
//
// The whole document is parsed before anything is assembled, because SDL is
// order-independent: a type may be extended above the line that defines it, and
// a `schema` block may name root types declared at the bottom of the file. A
// single pass that acted as it read would have to guess at both.
func parseSDL(source []byte) (*Schema, []string, error) {
	tokens, err := tokenise(source)
	if err != nil {
		return nil, nil, err
	}

	p := &parser{tokens: tokens, warn: &warnings{}}
	document, err := p.parseDocument()
	if err != nil {
		return nil, nil, err
	}

	schema := document.assemble(p.warn)
	finalise(schema, p.warn)
	return schema, p.warn.list(), nil
}

type parser struct {
	tokens []token
	pos    int
	warn   *warnings
}

// sdlDocument is the document as written, before extensions are folded in and
// the implicit root types are filled in.
type sdlDocument struct {
	definitions []*definition
	roots       map[string]rootDeclaration
	schemaSeen  bool
	description string
}

type definition struct {
	typ *Type
	// keyword is the word the document used — `type`, `input` and so on — kept
	// for messages, which should quote the schema rather than this package's
	// vocabulary for it.
	keyword string
	extend  bool
	tok     token
}

type rootDeclaration struct {
	typeName string
	tok      token
}

func (p *parser) current() token { return p.tokens[p.pos] }

func (p *parser) advance() token {
	tok := p.tokens[p.pos]
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return tok
}

func (p *parser) at(kind tokenKind) bool { return p.current().kind == kind }

func (p *parser) atPunct(text string) bool {
	tok := p.current()
	return tok.kind == tokenPunct && tok.text == text
}

func (p *parser) atName(text string) bool {
	tok := p.current()
	return tok.kind == tokenName && tok.text == text
}

func (p *parser) expectPunct(text string) (token, error) {
	if !p.atPunct(text) {
		return token{}, p.unexpected("`" + text + "`")
	}
	return p.advance(), nil
}

func (p *parser) expectName(what string) (token, error) {
	if !p.at(tokenName) {
		return token{}, p.unexpected(what)
	}
	return p.advance(), nil
}

func (p *parser) unexpected(what string) error {
	tok := p.current()
	return positionError(tok.line, tok.col, "expected %s, found %s", what, tok.describe())
}

func (p *parser) errorAt(tok token, format string, args ...any) error {
	return positionError(tok.line, tok.col, format, args...)
}

func (p *parser) parseDocument() (*sdlDocument, error) {
	document := &sdlDocument{roots: map[string]rootDeclaration{}}

	for !p.at(tokenEOF) {
		description := ""
		if p.at(tokenString) {
			description = p.advance().text
		}

		extend := false
		if p.atName("extend") {
			extend = true
			p.advance()
			if description != "" {
				// Not silently dropped: the author wrote documentation that
				// will not appear anywhere, and the reason is a rule of the
				// language rather than anything vertrag chose.
				p.warn.add("a description was written before `extend`, where the language does not " +
					"allow one; it is dropped, and belongs on the definition being extended")
			}
		}

		keyword := p.current()
		if keyword.kind != tokenName {
			return nil, p.unexpected("a type definition")
		}

		switch keyword.text {
		case "schema":
			if err := p.parseSchemaDefinition(document, extend, description); err != nil {
				return nil, err
			}

		case "scalar", "type", "interface", "union", "enum", "input":
			def, err := p.parseTypeDefinition(description, extend)
			if err != nil {
				return nil, err
			}
			document.definitions = append(document.definitions, def)

		case "directive":
			if extend {
				return nil, p.errorAt(keyword, "a directive definition cannot be extended")
			}
			if err := p.parseDirectiveDefinition(); err != nil {
				return nil, err
			}

		case "query", "mutation", "subscription", "fragment":
			// The commonest wrong file to hand a GraphQL tool, and worth
			// saying plainly: an operation document and a schema are both
			// `.graphql` and look alike at a glance.
			return nil, p.errorAt(keyword,
				"`%s` starts a GraphQL operation rather than a type definition: this is a query "+
					"document, and vertrag needs the schema itself — either SDL or the result of "+
					"the introspection query", keyword.text)

		default:
			return nil, p.errorAt(keyword, "`%s` does not start a GraphQL type definition", keyword.text)
		}
	}

	return document, nil
}

// parseSchemaDefinition reads `schema { query: ... }` and its extension form.
func (p *parser) parseSchemaDefinition(document *sdlDocument, extend bool, description string) error {
	p.advance() // schema

	directives, err := p.parseDirectives()
	if err != nil {
		return err
	}
	for _, applied := range directives {
		p.warn.noteDirective(applied.name, "the schema definition")
	}

	if description != "" && document.description == "" {
		document.description = description
	}

	if !p.atPunct("{") {
		// `extend schema @directive` with no block is a legal extension: it
		// adds directives and nothing else.
		if extend && len(directives) > 0 {
			return nil
		}
		return p.unexpected("`{`")
	}
	if !extend {
		if document.schemaSeen {
			p.warn.add("this document has more than one `schema` definition, which the language " +
				"does not allow; the root types named by both are taken together")
		}
		document.schemaSeen = true
	}
	p.advance()

	for !p.atPunct("}") {
		if p.at(tokenEOF) {
			return p.unexpected("`}`")
		}

		operation, err := p.expectName("`query`, `mutation` or `subscription`")
		if err != nil {
			return err
		}
		switch operation.text {
		case "query", "mutation", "subscription":
		default:
			return p.errorAt(operation,
				"`%s` is not a root operation type; GraphQL has query, mutation and subscription",
				operation.text)
		}

		if _, err := p.expectPunct(":"); err != nil {
			return err
		}
		named, err := p.expectName("a type name")
		if err != nil {
			return err
		}

		if prior, seen := document.roots[operation.text]; seen {
			p.warn.add("the %s root type is declared twice, as %s and as %s; the first is used",
				operation.text, prior.typeName, named.text)
			continue
		}
		document.roots[operation.text] = rootDeclaration{typeName: named.text, tok: named}
	}
	p.advance()

	return nil
}

// parseTypeDefinition reads any of the six named-type definitions, and their
// extension forms, which differ only in the keyword before them.
func (p *parser) parseTypeDefinition(description string, extend bool) (*definition, error) {
	keyword := p.advance()

	name, err := p.expectName("a type name")
	if err != nil {
		return nil, err
	}

	typ := &Type{Name: name.text, Description: description}
	switch keyword.text {
	case "scalar":
		typ.Kind = KindScalar
	case "type":
		typ.Kind = KindObject
	case "interface":
		typ.Kind = KindInterface
	case "union":
		typ.Kind = KindUnion
	case "enum":
		typ.Kind = KindEnum
	case "input":
		typ.Kind = KindInputObject
	}

	// An interface may implement interfaces, which the 2018 specification added
	// and which schemas in the wild do use.
	if typ.Kind == KindObject || typ.Kind == KindInterface {
		if p.atName("implements") {
			p.advance()
			interfaces, err := p.parseImplements()
			if err != nil {
				return nil, err
			}
			typ.Interfaces = interfaces
		}
	}

	directives, err := p.parseDirectives()
	if err != nil {
		return nil, err
	}
	p.applyTypeDirectives(typ, directives)

	switch typ.Kind {
	case KindObject, KindInterface:
		err = p.parseFieldDefinitions(typ)
	case KindInputObject:
		err = p.parseInputFieldDefinitions(typ)
	case KindEnum:
		err = p.parseEnumValues(typ)
	case KindUnion:
		var members []string
		if members, err = p.parseUnionMembers(); err == nil {
			typ.Possible = members
		}
	}
	if err != nil {
		return nil, err
	}

	return &definition{typ: typ, keyword: keyword.text, extend: extend, tok: name}, nil
}

// parseImplements reads the interface list after `implements`.
//
// Two spellings exist. The current one separates names with `&`; the original
// used commas, which the lexer discards as an ignored token, so that form
// arrives here as bare names in a row. Accepting a following name would be
// ambiguous — `type A implements B` followed by the next definition looks the
// same — so the token's record of whether a comma preceded it decides, which is
// exactly the information the two spellings differ by.
func (p *parser) parseImplements() ([]string, error) {
	if p.atPunct("&") {
		p.advance()
	}

	var names []string
	for {
		name, err := p.expectName("an interface name")
		if err != nil {
			return nil, err
		}
		names = append(names, name.text)

		if p.atPunct("&") {
			p.advance()
			continue
		}
		if p.at(tokenName) && p.current().afterComma {
			continue
		}
		return names, nil
	}
}

func (p *parser) parseFieldDefinitions(typ *Type) error {
	// A type with no braces at all is legal, and is how an extension that adds
	// only an interface or a directive is written.
	if !p.atPunct("{") {
		return nil
	}
	p.advance()

	for !p.atPunct("}") {
		if p.at(tokenEOF) {
			return p.unexpected("`}`")
		}

		description := ""
		if p.at(tokenString) {
			description = p.advance().text
		}

		name, err := p.expectName("a field name")
		if err != nil {
			return err
		}
		field := Field{Name: name.text, Description: description}
		where := typ.Name + "." + field.Name

		if p.atPunct("(") {
			args, err := p.parseArgumentDefinitions(where)
			if err != nil {
				return err
			}
			field.Args = args
		}

		if _, err := p.expectPunct(":"); err != nil {
			return err
		}
		if field.Type, err = p.parseTypeRef(); err != nil {
			return err
		}

		directives, err := p.parseDirectives()
		if err != nil {
			return err
		}
		p.applyFieldDirectives(&field, "the field "+where, directives)

		typ.Fields = append(typ.Fields, field)
	}
	p.advance()

	return nil
}

// parseInputFieldDefinitions reads an input object's fields.
//
// An input field is grammatically an argument: it takes a default value and no
// arguments of its own, where an object field is the other way round.
func (p *parser) parseInputFieldDefinitions(typ *Type) error {
	if !p.atPunct("{") {
		return nil
	}
	p.advance()

	for !p.atPunct("}") {
		if p.at(tokenEOF) {
			return p.unexpected("`}`")
		}

		description := ""
		if p.at(tokenString) {
			description = p.advance().text
		}

		name, err := p.expectName("an input field name")
		if err != nil {
			return err
		}
		if _, err := p.expectPunct(":"); err != nil {
			return err
		}

		field := Field{Name: name.text, Description: description}
		if field.Type, err = p.parseTypeRef(); err != nil {
			return err
		}

		if p.atPunct("=") {
			p.advance()
			value, err := p.parseValue()
			if err != nil {
				return err
			}
			field.Default, field.HasDefault = value, true
		}

		directives, err := p.parseDirectives()
		if err != nil {
			return err
		}
		p.applyFieldDirectives(&field, "the input field "+typ.Name+"."+field.Name, directives)

		attachInputDefault(&field)
		typ.Fields = append(typ.Fields, field)
	}
	p.advance()

	return nil
}

// attachInputDefault mirrors an input field's default into its Args.
//
// See Field.Default for why both representations are filled in: Argument is
// documented as carrying "an input-object field's default", and Args is the
// only place on a Field an Argument can sit, so a consumer may reasonably look
// in either place.
func attachInputDefault(field *Field) {
	if !field.HasDefault {
		return
	}
	field.Args = append(field.Args, Argument{
		Name:              field.Name,
		Description:       field.Description,
		Type:              field.Type,
		Default:           field.Default,
		HasDefault:        true,
		Deprecated:        field.Deprecated,
		DeprecationReason: field.DeprecationReason,
	})
}

func (p *parser) parseArgumentDefinitions(where string) ([]Argument, error) {
	p.advance() // (

	var args []Argument
	for !p.atPunct(")") {
		if p.at(tokenEOF) {
			return nil, p.unexpected("`)`")
		}

		description := ""
		if p.at(tokenString) {
			description = p.advance().text
		}

		name, err := p.expectName("an argument name")
		if err != nil {
			return nil, err
		}
		if _, err := p.expectPunct(":"); err != nil {
			return nil, err
		}

		arg := Argument{Name: name.text, Description: description}
		if arg.Type, err = p.parseTypeRef(); err != nil {
			return nil, err
		}

		if p.atPunct("=") {
			p.advance()
			value, err := p.parseValue()
			if err != nil {
				return nil, err
			}
			arg.Default, arg.HasDefault = value, true
		}

		directives, err := p.parseDirectives()
		if err != nil {
			return nil, err
		}
		p.applyArgumentDirectives(&arg, fmt.Sprintf("the argument %s(%s:)", where, arg.Name), directives)

		args = append(args, arg)
	}
	p.advance()

	return args, nil
}

func (p *parser) parseEnumValues(typ *Type) error {
	if !p.atPunct("{") {
		return nil
	}
	p.advance()

	for !p.atPunct("}") {
		if p.at(tokenEOF) {
			return p.unexpected("`}`")
		}

		description := ""
		if p.at(tokenString) {
			description = p.advance().text
		}

		name, err := p.expectName("an enum value")
		if err != nil {
			return err
		}
		switch name.text {
		case "true", "false", "null":
			return p.errorAt(name,
				"`%s` cannot be an enum value: it is a literal, and a schema using it as a value "+
					"is ambiguous everywhere that value is written", name.text)
		}

		directives, err := p.parseDirectives()
		if err != nil {
			return err
		}

		typ.EnumValues = append(typ.EnumValues, name.text)
		if description != "" {
			if typ.EnumValueDescriptions == nil {
				typ.EnumValueDescriptions = map[string]string{}
			}
			typ.EnumValueDescriptions[name.text] = description
		}

		for _, applied := range directives {
			if applied.name == "deprecated" {
				typ.DeprecatedEnumValues = append(typ.DeprecatedEnumValues, name.text)
				continue
			}
			p.warn.noteDirective(applied.name, "the enum value "+typ.Name+"."+name.text)
		}
	}
	p.advance()

	return nil
}

func (p *parser) parseUnionMembers() ([]string, error) {
	if !p.atPunct("=") {
		return nil, nil
	}
	p.advance()

	// A leading `|` is allowed, so a long union can be written one member per
	// line with every line looking the same.
	if p.atPunct("|") {
		p.advance()
	}

	var members []string
	for {
		name, err := p.expectName("a union member type name")
		if err != nil {
			return nil, err
		}
		members = append(members, name.text)

		if p.atPunct("|") {
			p.advance()
			continue
		}
		return members, nil
	}
}

// parseTypeRef reads a type reference and its wrappers.
//
// The wrappers nest outward: `[Foo!]!` is a non-null list of non-null Foo, and
// the two `!` mean different things. Flattening them into a single "required"
// flag — which is the tempting simplification, because that is all OpenAPI has
// — would lose the difference between a list that may be null and a list whose
// members may be.
func (p *parser) parseTypeRef() (TypeRef, error) {
	var ref TypeRef

	if p.atPunct("[") {
		p.advance()
		inner, err := p.parseTypeRef()
		if err != nil {
			return TypeRef{}, err
		}
		if _, err := p.expectPunct("]"); err != nil {
			return TypeRef{}, err
		}
		ref = TypeRef{List: &inner}
	} else {
		name, err := p.expectName("a type name")
		if err != nil {
			return TypeRef{}, err
		}
		ref = TypeRef{Named: name.text}
	}

	if p.atPunct("!") {
		p.advance()
		ref.NonNull = true
	}
	return ref, nil
}

// appliedDirective is one `@name(args)` written on something.
type appliedDirective struct {
	name string
	args map[string]any
	tok  token
}

func (p *parser) parseDirectives() ([]appliedDirective, error) {
	var applied []appliedDirective

	for p.atPunct("@") {
		at := p.advance()
		name, err := p.expectName("a directive name")
		if err != nil {
			return nil, err
		}

		directive := appliedDirective{name: name.text, args: map[string]any{}, tok: at}
		if p.atPunct("(") {
			p.advance()
			for !p.atPunct(")") {
				if p.at(tokenEOF) {
					return nil, p.unexpected("`)`")
				}
				argument, err := p.expectName("an argument name")
				if err != nil {
					return nil, err
				}
				if _, err := p.expectPunct(":"); err != nil {
					return nil, err
				}
				value, err := p.parseValue()
				if err != nil {
					return nil, err
				}
				directive.args[argument.text] = value
			}
			p.advance()
		}
		applied = append(applied, directive)
	}

	return applied, nil
}

// parseDirectiveDefinition reads `directive @name(...) on LOCATION | ...`.
//
// Nothing is kept. A directive definition tells you a directive exists and
// where it may be written; it says nothing about what it DOES, which lives in
// the server's resolvers. That is precisely why the definition is warned about:
// a schema that defines @auth is a schema where some fields behave differently
// from how they read, and a test run that never mentions it looks complete.
func (p *parser) parseDirectiveDefinition() error {
	p.advance() // directive

	if _, err := p.expectPunct("@"); err != nil {
		return err
	}
	name, err := p.expectName("a directive name")
	if err != nil {
		return err
	}

	if p.atPunct("(") {
		if _, err := p.parseArgumentDefinitions("@" + name.text); err != nil {
			return err
		}
	}
	if p.atName("repeatable") {
		p.advance()
	}
	if !p.atName("on") {
		return p.unexpected("`on`")
	}
	p.advance()

	if p.atPunct("|") {
		p.advance()
	}
	for {
		if _, err := p.expectName("a directive location"); err != nil {
			return err
		}
		if p.atPunct("|") {
			p.advance()
			continue
		}
		break
	}

	if !isBuiltInDirective(name.text) {
		p.warn.add("the schema defines the directive @%s; vertrag does not model it, so whatever "+
			"it changes about this API goes untested", name.text)
	}
	return nil
}

// isBuiltInDirective reports whether a directive is one the language defines,
// which is the set worth staying quiet about: they are in every schema, and
// three of the five are about executing a query rather than describing an API.
func isBuiltInDirective(name string) bool {
	switch name {
	case "skip", "include", "deprecated", "specifiedBy", "oneOf":
		return true
	}
	return false
}

// applyTypeDirectives acts on the directives a type definition carries, and
// reports the ones it cannot act on.
func (p *parser) applyTypeDirectives(typ *Type, directives []appliedDirective) {
	for _, applied := range directives {
		switch applied.name {
		case "oneOf":
			if typ.Kind != KindInputObject {
				p.warn.add("@oneOf is written on %s, which is %s %s rather than an input object; "+
					"it is ignored", typ.Name, article(typ.Kind.String()), typ.Kind)
				continue
			}
			typ.OneOf = true

		case "specifiedBy":
			url, _ := applied.args["url"].(string)
			if url == "" {
				p.warn.add("@specifiedBy on %s has no `url` argument, so it says nothing about "+
					"what values of this scalar look like", typ.Name)
				continue
			}
			typ.SpecifiedBy = url

		default:
			p.warn.noteDirective(applied.name, "the type "+typ.Name)
		}
	}
}

func (p *parser) applyFieldDirectives(field *Field, where string, directives []appliedDirective) {
	for _, applied := range directives {
		if applied.name == "deprecated" {
			field.Deprecated = true
			field.DeprecationReason = deprecationReason(applied)
			continue
		}
		p.warn.noteDirective(applied.name, where)
	}
}

func (p *parser) applyArgumentDirectives(arg *Argument, where string, directives []appliedDirective) {
	for _, applied := range directives {
		if applied.name == "deprecated" {
			arg.Deprecated = true
			arg.DeprecationReason = deprecationReason(applied)
			continue
		}
		p.warn.noteDirective(applied.name, where)
	}
}

// deprecationReason reads the directive's argument, falling back to the default
// the specification gives it — which is a real string, not an empty one, and is
// what a server's own introspection reports for `@deprecated` with no reason.
func deprecationReason(applied appliedDirective) string {
	if reason, ok := applied.args["reason"].(string); ok {
		return reason
	}
	return "No longer supported"
}

// assemble folds the document into a schema: definitions first, then the
// extensions, so that an extension written above its definition still finds it.
func (d *sdlDocument) assemble(w *warnings) *Schema {
	s := &Schema{Types: map[string]*Type{}, Description: d.description}

	for _, def := range d.definitions {
		if def.extend {
			continue
		}
		existing, defined := s.Types[def.typ.Name]
		if !defined {
			s.Types[def.typ.Name] = def.typ
			continue
		}
		w.add("the type %s is defined twice; the definitions are merged, which matches neither "+
			"of them exactly", def.typ.Name)
		mergeType(existing, def.typ, w)
	}

	for _, def := range d.definitions {
		if !def.extend {
			continue
		}
		existing, defined := s.Types[def.typ.Name]
		if !defined {
			// The language calls this invalid. Reading it as the definition is
			// the useful answer — an extension of a type assembled elsewhere is
			// how federated schemas are written, and the alternative is
			// discarding every field it declares.
			w.add("`extend %s %s` has nothing to extend, because this schema never defines %s; "+
				"the extension is read as the definition", def.keyword, def.typ.Name, def.typ.Name)
			s.Types[def.typ.Name] = def.typ
			continue
		}
		if existing.Kind != def.typ.Kind {
			w.add("`extend %s %s` does not match the definition of %s, which is %s %s; the "+
				"extension is ignored", def.keyword, def.typ.Name, def.typ.Name,
				article(existing.Kind.String()), existing.Kind)
			continue
		}
		mergeType(existing, def.typ, w)
	}

	linkInterfaces(s, w)

	for _, root := range conventionalRoots {
		if declared, ok := d.roots[root.operation]; ok {
			*root.field(s) = declared.typeName
		}
	}
	return s
}

// mergeType folds an extension, or a second definition, into the type it names.
func mergeType(into, extra *Type, w *warnings) {
	present := make(map[string]bool, len(into.Fields))
	for _, field := range into.Fields {
		present[field.Name] = true
	}
	for _, field := range extra.Fields {
		if present[field.Name] {
			w.add("%s is given the field %s twice; the first definition is used",
				into.Name, field.Name)
			continue
		}
		into.Fields = append(into.Fields, field)
		present[field.Name] = true
	}

	into.EnumValues = appendMissing(into.EnumValues, extra.EnumValues)
	into.DeprecatedEnumValues = appendMissing(into.DeprecatedEnumValues, extra.DeprecatedEnumValues)
	into.Possible = appendMissing(into.Possible, extra.Possible)
	into.Interfaces = appendMissing(into.Interfaces, extra.Interfaces)

	for name, description := range extra.EnumValueDescriptions {
		if into.EnumValueDescriptions == nil {
			into.EnumValueDescriptions = map[string]string{}
		}
		if _, described := into.EnumValueDescriptions[name]; !described {
			into.EnumValueDescriptions[name] = description
		}
	}

	if extra.OneOf {
		into.OneOf = true
	}
	if extra.SpecifiedBy != "" {
		into.SpecifiedBy = extra.SpecifiedBy
	}
	if into.Description == "" {
		into.Description = extra.Description
	}
}

func appendMissing(into, extra []string) []string {
	present := make(map[string]bool, len(into))
	for _, name := range into {
		present[name] = true
	}
	for _, name := range extra {
		if present[name] {
			continue
		}
		into = append(into, name)
		present[name] = true
	}
	return into
}

// linkInterfaces fills in each interface's list of the objects implementing it.
//
// SDL states the relationship from the object's side only, so the reverse has
// to be built — and it is the direction that matters to a consumer, which needs
// a concrete type before it can ask for anything through an interface.
//
// The objects are visited in name order because the map they come from has no
// order, and Possible would otherwise differ between runs of the same schema.
func linkInterfaces(s *Schema, w *warnings) {
	for _, name := range sortedTypeNames(s.Types) {
		typ := s.Types[name]
		if typ.Kind != KindObject {
			continue
		}

		for _, ifaceName := range transitiveInterfaces(s, typ) {
			iface, defined := s.Types[ifaceName]
			if !defined {
				// Reported by the cross-check, which names every dangling
				// reference in one place.
				continue
			}
			if iface.Kind != KindInterface {
				w.add("%s says it implements %s, which is %s %s rather than an interface",
					name, ifaceName, article(iface.Kind.String()), iface.Kind)
				continue
			}
			iface.Possible = appendMissing(iface.Possible, []string{name})
		}
	}
}

// transitiveInterfaces follows interfaces that themselves implement interfaces.
//
// An object implementing `Dog` where `Dog implements Animal` is required by the
// language to declare both, but a schema that declares only the first is common
// enough — and reading it strictly would leave `Animal` with no possible types
// at all, which is worse than being lenient here.
func transitiveInterfaces(s *Schema, typ *Type) []string {
	var out []string
	seen := map[string]bool{}

	queue := append([]string(nil), typ.Interfaces...)
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)

		if next, defined := s.Types[name]; defined {
			queue = append(queue, next.Interfaces...)
		}
	}
	return out
}
