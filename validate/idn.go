package validate

import (
	"fmt"
	"net/netip"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/santhosh-tekuri/jsonschema/v6"
	schemakind "github.com/santhosh-tekuri/jsonschema/v6/kind"
	"golang.org/x/net/idna"
)

var idnProfile = idna.New(idna.MapForLookup(), idna.Transitional(false), idna.BidiRule(), idna.VerifyDNSLength(true))

var idnFormats = map[string]*jsonschema.Format{
	"idn-email":    {Name: "idn-email", Validate: validateIDNEmail},
	"idn-hostname": {Name: "idn-hostname", Validate: validateIDNHostname},
}

func registerIDNFormats(compiler *jsonschema.Compiler) {
	for _, format := range idnFormats {
		compiler.RegisterFormat(format)
	}
	compiler.AssertVocabs()
	compiler.RegisterVocabulary(&jsonschema.Vocabulary{
		URL: "https://github.com/antimatter-studios/vertrag/vocab/idn-formats",
		Compile: func(ctx *jsonschema.CompilerContext, schema map[string]any) (jsonschema.SchemaExt, error) {
			compiled := ctx.Enqueue(nil)
			if compiled.DraftVersion < 2019 || compiled.Format != nil {
				return nil, nil
			}
			name, _ := schema["format"].(string)
			if format := idnFormats[name]; format != nil {
				return idnFormatAssertion{format}, nil
			}
			return nil, nil
		},
	})
}

type idnFormatAssertion struct {
	format *jsonschema.Format
}

func (assertion idnFormatAssertion) Validate(ctx *jsonschema.ValidatorContext, value any) {
	if err := assertion.format.Validate(value); err != nil {
		ctx.AddError(&schemakind.Format{Got: value, Want: assertion.format.Name, Err: err})
	}
}

func validateIDNHostname(value any) error {
	hostname, ok := value.(string)
	if !ok {
		return nil
	}
	if !utf8.ValidString(hostname) {
		return fmt.Errorf("hostname is not valid UTF-8")
	}
	ascii, err := idnProfile.ToASCII(hostname)
	if err != nil {
		return err
	}
	if ascii == "" || strings.HasSuffix(ascii, ".") {
		return fmt.Errorf("hostname has an empty label")
	}
	decoded, err := idnProfile.ToUnicode(ascii)
	if err != nil {
		return err
	}
	for _, label := range strings.Split(decoded, ".") {
		if err := validateIDNLabel(label); err != nil {
			return err
		}
	}
	return nil
}

func validateIDNLabel(label string) error {
	characters := []rune(label)
	hasJapanese := strings.ContainsFunc(label, func(character rune) bool {
		return unicode.In(character, unicode.Hiragana, unicode.Katakana, unicode.Han)
	})
	for index, character := range characters {
		valid := true
		switch character {
		case '-', '\u00df', '\u03c2', '\u06fd', '\u06fe', '\u0f0b', '\u3007', '\u200c', '\u200d':
		case '\u0640', '\u07fa', '\u302e', '\u302f', '\u3031', '\u3032', '\u3033', '\u3034', '\u3035', '\u303b':
			valid = false
		case '\u00b7':
			valid = index > 0 && index+1 < len(characters) && characters[index-1] == 'l' && characters[index+1] == 'l'
		case '\u0375':
			valid = index+1 < len(characters) && unicode.In(characters[index+1], unicode.Greek)
		case '\u05f3', '\u05f4':
			valid = index > 0 && unicode.In(characters[index-1], unicode.Hebrew)
		case '\u30fb':
			valid = hasJapanese
		default:
			valid = unicode.In(character, unicode.Ll, unicode.Lu, unicode.Lo, unicode.Lm, unicode.Mn, unicode.Mc, unicode.Nd)
		}
		if !valid {
			return fmt.Errorf("invalid IDNA2008 character %U in label", character)
		}
	}
	return nil
}

func validateIDNEmail(value any) error {
	address, ok := value.(string)
	if !ok {
		return nil
	}
	if !utf8.ValidString(address) {
		return fmt.Errorf("email address is not valid UTF-8")
	}
	if len(address) > 254 {
		return fmt.Errorf("email address exceeds 254 octets")
	}
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return fmt.Errorf("email address is missing @")
	}
	local, domain := address[:at], address[at+1:]
	if err := validateIDNLocal(local); err != nil {
		return err
	}
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		literal := domain[1 : len(domain)-1]
		literal, ipv6 := strings.CutPrefix(literal, "IPv6:")
		parsed, err := netip.ParseAddr(literal)
		if err != nil || parsed.Zone() != "" || (ipv6 && !parsed.Is6()) || (!ipv6 && !parsed.Is4()) {
			return fmt.Errorf("invalid email address literal")
		}
		return nil
	}
	if strings.HasSuffix(domain, ".") {
		return fmt.Errorf("email domain ends with a dot")
	}
	if err := validateIDNHostname(domain); err != nil {
		return fmt.Errorf("invalid email domain: %w", err)
	}
	return nil
}

func validateIDNLocal(local string) error {
	if len(local) == 0 || len(local) > 64 {
		return fmt.Errorf("email local part must be 1 to 64 octets")
	}
	if len(local) >= 2 && local[0] == '"' && local[len(local)-1] == '"' {
		escaped := false
		for _, character := range local[1 : len(local)-1] {
			if escaped {
				if character < 32 || character > 126 {
					return fmt.Errorf("invalid quoted pair in email local part")
				}
				escaped = false
				continue
			}
			if character == '\\' {
				escaped = true
				continue
			}
			if character < 32 || character == '"' || character == 127 {
				return fmt.Errorf("invalid character in quoted email local part")
			}
		}
		if escaped {
			return fmt.Errorf("unfinished quoted pair in email local part")
		}
		return nil
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return fmt.Errorf("email local part has an empty atom")
	}
	for _, character := range local {
		if character >= utf8.RuneSelf || (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune(".!#$%&'*+-/=?^_`{|}~", character) {
			continue
		}
		return fmt.Errorf("invalid character in email local part")
	}
	return nil
}
