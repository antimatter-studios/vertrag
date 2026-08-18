package validate

import "testing"

// TestAStatusRangeMatchesItsWholeBand pins the comparison a range needs. A
// band cannot be compared for equality, and comparing it that way is how a
// `2XX` transaction came to expect a status no server can ever send.
func TestAStatusRangeMatchesItsWholeBand(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected string
		real     string
		matches  bool
	}{
		{"the lowest code in the band", "2XX", "200", true},
		{"the highest code in the band", "2XX", "299", true},
		{"a code inside the band", "2XX", "204", true},
		{"a code in another band", "2XX", "404", false},
		{"a client-error band", "4XX", "422", true},
		{"a server-error band", "5XX", "503", true},
		{"an exact code is still exact", "200", "204", false},
		{"surrounding whitespace is ignored", " 2XX ", " 201 ", true},
		{"a range is not a status a server can send", "2XX", "2XX", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := StatusMatches(test.expected, test.real); got != test.matches {
				t.Errorf("StatusMatches(%q, %q) = %v, want %v",
					test.expected, test.real, got, test.matches)
			}
		})
	}
}

// TestARangedExpectationFailsWithTheRangeInTheMessage keeps the failure text
// actionable: the reader has to see which band was expected, not a code the
// document never wrote.
func TestARangedExpectationFailsWithTheRangeInTheMessage(t *testing.T) {
	result := Validate(Message{StatusCode: "2XX"}, Message{StatusCode: "404"})

	if result.Valid {
		t.Fatalf("404 satisfied a 2XX expectation")
	}
	want := "Expected status code '2XX', but got '404'."
	if got := result.Fields["statusCode"].Errors[0]; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
}

func TestAStatusInTheBandSatisfiesARangedExpectation(t *testing.T) {
	if result := Validate(Message{StatusCode: "2XX"}, Message{StatusCode: "201"}); !result.Valid {
		t.Errorf("201 did not satisfy a 2XX expectation: %v", result.Fields["statusCode"].Errors)
	}
}
