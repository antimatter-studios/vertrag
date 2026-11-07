package validate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var idnDialects = []string{
	"",
	"http://json-schema.org/draft-04/schema#",
	"http://json-schema.org/draft-06/schema#",
	"http://json-schema.org/draft-07/schema#",
	"https://json-schema.org/draft/2019-09/schema",
	"https://json-schema.org/draft/2020-12/schema",
}

func TestIDNFormats(t *testing.T) {
	longDomain := strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61)
	for _, format := range []string{"idn-email", "idn-hostname"} {
		tests := []struct {
			value string
			valid bool
		}{
			{"", false},
			{"a", true},
			{"example.com", true},
			{"EXAMPLE.COM", true},
			{"例子.广告", true},
			{"xn--fsqu00a.xn--3lr804guic", true},
			{"xn--.example", false},
			{"xn--a.example", false},
			{"-bad.example", false},
			{"bad-.example", false},
			{"ab--cd.example", false},
			{"bad_name.example", false},
			{"bad..example", false},
			{".example", false},
			{"bad name.example", false},
			{"K.example", true},
			{"e\u0301.example", true},
			{"１２３.example", true},
			{"a\u200bb.example", true},
			{"a。b", true},
			{"a．b", true},
			{"a｡b", true},
			{"a。", false},
			{"실〮례.테스트", false},
			{"ـߺ", false},
			{"〱〲〳〴〵〮〯〻", false},
			{"a·l", false},
			{"·l", false},
			{"l·a", false},
			{"l·", false},
			{"α͵s", false},
			{"α͵", false},
			{"׳ב", false},
			{"״ב", false},
			{"def・abc", false},
			{"・", false},
			{"😀.example", false},
			{"xn--7a", false},
			{"l·l", true},
			{"α͵β", true},
			{"א׳ב", true},
			{"א״ב", true},
			{"・ぁ", true},
			{"・ァ", true},
			{"・丈", true},
			{"\u0301bad.example", false},
			{"bad\u200d.example", false},
			{"אa.example", false},
			{strings.Repeat("a", 64) + ".example", false},
			{strings.Repeat("例", 64) + ".example", false},
			{longDomain + "e", false},
		}
		if format == "idn-email" {
			for index := range tests {
				tests[index].value = "ascii@" + tests[index].value
			}
			tests = append(tests, []struct {
				value string
				valid bool
			}{
				{"a", false},
				{"someone@example.com", true},
				{"用户@例子.广告", true},
				{"ascii@xn--fsqu00a.xn--3lr804guic", true},
				{"用户@example.com", true},
				{"ascii@例子.广告", true},
				{"first.last+tag@example.com", true},
				{`"用户 name"@例子.广告`, true},
				{`"a@b"@example.com`, true},
				{`"a\"b"@example.com`, true},
				{"ascii@[192.0.2.1]", true},
				{"用户@[IPv6:2001:db8::1]", true},
				{strings.Repeat("a", 64) + "@example.com", true},
				{strings.Repeat("用", 21) + "@example.com", true},
				{"ascii@", false},
				{"@example.com", false},
				{"a@@example.com", false},
				{".a@example.com", false},
				{"a.@example.com", false},
				{"a..b@example.com", false},
				{"a b@example.com", false},
				{"a\n@example.com", false},
				{"Name <a@example.com>", false},
				{"a(comment)@example.com", false},
				{`"unclosed@example.com`, false},
				{`"a\"@example.com`, false},
				{`"a\用户"@example.com`, false},
				{"ascii@example.com.", false},
				{"ascii@[bad]", false},
				{"ascii@[IPv6:192.0.2.1]", false},
				{"ascii@[IPv6:fe80::1%eth0]", false},
				{strings.Repeat("a", 65) + "@example.com", false},
				{strings.Repeat("用", 22) + "@example.com", false},
				{"ascii@" + longDomain, false},
			}...)
		} else {
			tests = append(tests, []struct {
				value string
				valid bool
			}{
				{"example.com.", false},
				{"例子.广告.", false},
				{strings.Repeat("a", 63) + ".example", true},
				{longDomain, true},
				{longDomain + ".", false},
				{".", false},
				{"example.com..", false},
				{"https://example.com", false},
				{"someone@example.com", false},
			}...)
		}
		for _, dialect := range idnDialects {
			t.Run(format+"/"+dialect, func(t *testing.T) {
				schema, err := json.Marshal(map[string]any{"$schema": dialect, "format": format})
				if err != nil {
					t.Fatal(err)
				}
				if dialect == "" {
					schema = fmt.Appendf(nil, `{"format":%q}`, format)
				}
				if err := Usable(schema); err != nil {
					t.Fatal(err)
				}
				for _, test := range tests {
					t.Run(test.value, func(t *testing.T) {
						encoded, _ := json.Marshal(test.value)
						result := AgainstSchema(schema, string(encoded))
						if result.Valid != test.valid {
							t.Errorf("valid = %v, want %v: %v", result.Valid, test.valid, result.Errors)
						}
						if !test.valid && len(result.Errors) != 1 {
							t.Errorf("want one format error, got %v", result.Errors)
						}
					})
				}
				for _, body := range []string{`42`, `null`, `{}`, `[]`, `true`} {
					if result := AgainstSchema(schema, body); !result.Valid {
						t.Errorf("format must ignore non-string %s: %v", body, result.Errors)
					}
				}
			})
		}
	}
}

func TestAliasSourceAddressMustBeAnIDNEmail(t *testing.T) {
	for _, dialect := range idnDialects {
		t.Run(dialect, func(t *testing.T) {
			schema := `{"type":"object","required":["source_address"],"properties":{"source_address":{"type":"string","format":"idn-email"}}}`
			if dialect != "" {
				schema = `{"$schema":` + fmt.Sprintf("%q", dialect) + `,` + schema[1:]
			}
			result := check(t, schema, `{"source_address":"a"}`)
			if result.Valid || len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "/source_address") || !strings.Contains(result.Errors[0], "idn-email") {
				t.Fatalf("invalid source address was not diagnosed: %+v", result)
			}
			for _, address := range []string{"ascii@example.com", "用户@例子.广告", "ascii@xn--fsqu00a.xn--3lr804guic"} {
				body := fmt.Sprintf(`{"source_address":%q}`, address)
				if result := check(t, schema, body); !result.Valid {
					t.Errorf("valid source address refused: %v", result.Errors)
				}
			}
		})
	}
}

func TestExistingFormatsKeepTheirDraftSemantics(t *testing.T) {
	for _, dialect := range idnDialects {
		for _, format := range []string{"email", "hostname", "uuid", "date-time"} {
			t.Run(dialect+"/"+format, func(t *testing.T) {
				schema := fmt.Sprintf(`{"format":%q}`, format)
				if dialect != "" {
					schema = `{"$schema":` + fmt.Sprintf("%q", dialect) + `,` + schema[1:]
				}
				want := strings.Contains(dialect, "2019-09") || strings.Contains(dialect, "2020-12")
				if result := check(t, schema, `"not a valid value"`); result.Valid != want {
					t.Errorf("existing format semantics changed: %+v", result)
				}
			})
		}
	}
}

func TestIDNFormatsInsideReferencesAndCompositions(t *testing.T) {
	for _, dialect := range idnDialects[1:] {
		t.Run(dialect, func(t *testing.T) {
			schema := fmt.Sprintf(`{
				"$schema": %q,
				"type": "object",
				"properties": {
					"address": {"$ref": "#/definitions/address"},
					"host": {"anyOf": [{"format": "idn-hostname"}, {"const": ""}]}
				},
				"definitions": {"address": {"allOf": [{"type":"string"}, {"format":"idn-email"}]}}
			}`, dialect)
			if result := check(t, schema, `{"address":"用户@例子.广告","host":"例子.广告"}`); !result.Valid {
				t.Errorf("valid references and compositions refused: %v", result.Errors)
			}
			if result := check(t, schema, `{"address":"a"}`); result.Valid {
				t.Fatal("a referenced IDN format was ignored")
			}
			if strings.Contains(dialect, "2019") || strings.Contains(dialect, "2020") {
				if result := check(t, schema, `{"host":"bad..example"}`); result.Valid {
					t.Fatal("an IDN format in anyOf was ignored")
				}
			}
		})
	}
	for _, dialect := range []string{"http://json-schema.org/draft-07/schema#", "https://json-schema.org/draft/2020-12/schema"} {
		schema := fmt.Sprintf(`{"$schema":%q,"not":{"format":"idn-email"}}`, dialect)
		if result := check(t, schema, `"a"`); !result.Valid {
			t.Errorf("an invalid email should satisfy not: %v", result.Errors)
		}
		if result := check(t, schema, `"用户@例子.广告"`); result.Valid {
			t.Error("a valid email must not satisfy not")
		}
	}
}

func TestIDNFormatsCheckResponsesAndHeaders(t *testing.T) {
	for _, dialect := range idnDialects[1:] {
		for _, format := range []string{"idn-email", "idn-hostname"} {
			schema := fmt.Sprintf(`{"$schema":%q,"type":"string","format":%q}`, dialect, format)
			expected := Message{StatusCode: "200", Headers: map[string]string{"content-type": "application/json"}, BodySchema: json.RawMessage(schema)}
			actual := Message{StatusCode: "200", Headers: expected.Headers, Body: `"bad..example"`}
			if result := Validate(expected, actual); result.Valid {
				t.Errorf("%s/%s: invalid response accepted", dialect, format)
			}
			if findings := headerFindings(t, schema, "bad..example"); len(findings) != 1 {
				t.Errorf("%s/%s: want one header failure, got %v", dialect, format, findings)
			}
		}
	}
}

func TestIDNFormatsRejectInvalidUTF8(t *testing.T) {
	for _, format := range idnFormats {
		if err := format.Validate(string([]byte{0xff})); err == nil {
			t.Errorf("%s accepted invalid UTF-8", format.Name)
		}
	}
}

func TestIDNRegistrationUsesEachSubschemasDraft(t *testing.T) {
	const schema = `{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"type":"object",
		"properties":{
			"modern":{"format":"uuid"},
			"legacy":{"$id":"legacy.json","$schema":"http://json-schema.org/draft-07/schema#","format":"uuid"},
			"address":{"$id":"address.json","$schema":"http://json-schema.org/draft-07/schema#","format":"idn-email"},
			"hostname":{"format":"idn-hostname"}
		}
	}`
	for _, test := range []struct {
		body  string
		valid bool
	}{
		{`{"modern":"not a uuid","address":"用户@例子.广告","hostname":"例子.广告"}`, true},
		{`{"legacy":"not a uuid"}`, false},
		{`{"address":"a"}`, false},
		{`{"hostname":"bad..example"}`, false},
	} {
		if result := check(t, schema, test.body); result.Valid != test.valid {
			t.Errorf("%s: valid=%v, want %v: %v", test.body, result.Valid, test.valid, result.Errors)
		}
	}
	const legacyRef = `{"$ref":"#/definitions/value","format":"idn-email","definitions":{"value":{"type":"string"}}}`
	if result := check(t, legacyRef, `"a"`); !result.Valid {
		t.Errorf("legacy $ref siblings must still be ignored: %v", result.Errors)
	}
}
