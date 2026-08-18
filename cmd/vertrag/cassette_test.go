package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestRunWritesAHARFileOfTheTrafficItSent goes through the built binary rather
// than the reporter package, because everything the package can get right is
// still worthless if the flag is unwired or the file is never created — and
// every unit test in the tree stays green through exactly that mistake.
//
// It is also the only place the timestamps are checked against a real run. The
// reporter tests pin them with a fixed clock, which cannot tell whether the
// runner records a start time at all: a Result that never set one would have
// every entry stamped with the moment the report was written, and a viewer would
// draw the whole suite as one stacked bar.
func TestRunWritesAHARFileOfTheTrafficItSent(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	path := filepath.Join(t.TempDir(), "run.har")
	output, code := runCommand(t, binary, "run",
		"--endpoint", endpoint, "--no-color", "--reporter", "har", "--output", path, description)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, output)
	}

	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no archive was written: %v", err)
	}

	var archive struct {
		Log struct {
			Version string `json:"version"`
			Creator struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"creator"`
			Entries []struct {
				StartedDateTime string `json:"startedDateTime"`
				Request         struct {
					Method string `json:"method"`
					URL    string `json:"url"`
				} `json:"request"`
				Response struct {
					Status int `json:"status"`
				} `json:"response"`
			} `json:"entries"`
		} `json:"log"`
	}
	if err := json.Unmarshal(source, &archive); err != nil {
		t.Fatalf("the archive does not parse as JSON: %v\n%s", err, source)
	}

	if archive.Log.Version != "1.2" || archive.Log.Creator.Name != "vertrag" {
		t.Errorf("log = version %q by %q", archive.Log.Version, archive.Log.Creator.Name)
	}
	if archive.Log.Creator.Version == "" {
		t.Error("the archive does not say which vertrag wrote it")
	}
	if len(archive.Log.Entries) == 0 {
		t.Fatalf("the archive holds no entries for a run that sent requests:\n%s", source)
	}

	for i, entry := range archive.Log.Entries {
		if !strings.HasPrefix(entry.Request.URL, endpoint) {
			t.Errorf("entry %d url = %q, want the absolute address under %s",
				i, entry.Request.URL, endpoint)
		}
		if entry.Response.Status == 0 {
			t.Errorf("entry %d recorded no response for a run against a live server", i)
		}

		started, err := time.Parse(time.RFC3339, entry.StartedDateTime)
		if err != nil {
			t.Fatalf("entry %d startedDateTime %q does not parse: %v", i, entry.StartedDateTime, err)
		}
		// A minute is loose enough for a slow machine and tight enough to catch
		// the two mistakes worth catching: a zero time, which lands in year 1,
		// and a time nobody recorded.
		if time.Since(started) > time.Minute || time.Since(started) < -time.Minute {
			t.Errorf("entry %d was stamped %s, which is not when this run happened",
				i, entry.StartedDateTime)
		}
	}
}

// TestRunWritesAVCRCassetteOfTheTrafficItSent is the same check for the format a
// replay library reads, through the built binary for the same reason.
func TestRunWritesAVCRCassetteOfTheTrafficItSent(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	path := filepath.Join(t.TempDir(), "run.yml")
	output, code := runCommand(t, binary, "run",
		"--endpoint", endpoint, "--no-color", "--reporter", "vcr", "--output", path, description)
	if code != 0 {
		t.Fatalf("exit = %d, want 0\n%s", code, output)
	}

	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("no cassette was written: %v", err)
	}

	var cassette struct {
		Interactions []struct {
			Request struct {
				Method string `yaml:"method"`
				URI    string `yaml:"uri"`
			} `yaml:"request"`
			Response struct {
				Status struct {
					Code int `yaml:"code"`
				} `yaml:"status"`
			} `yaml:"response"`
			RecordedAt string `yaml:"recorded_at"`
		} `yaml:"http_interactions"`
		RecordedWith string `yaml:"recorded_with"`
		RecordedFrom string `yaml:"recorded_from"`
	}
	if err := yaml.Unmarshal(source, &cassette); err != nil {
		t.Fatalf("the cassette does not parse as YAML: %v\n%s", err, source)
	}

	if !strings.HasPrefix(cassette.RecordedWith, "vertrag ") {
		t.Errorf("recorded_with = %q, want the tool and its version", cassette.RecordedWith)
	}
	if !strings.Contains(cassette.RecordedFrom, endpoint) {
		t.Errorf("recorded_from = %q, want the endpoint it was taken against", cassette.RecordedFrom)
	}
	if len(cassette.Interactions) == 0 {
		t.Fatalf("the cassette holds no interactions for a run that sent requests:\n%s", source)
	}

	for i, interaction := range cassette.Interactions {
		if !strings.HasPrefix(interaction.Request.URI, endpoint) {
			t.Errorf("interaction %d uri = %q, want the absolute address under %s",
				i, interaction.Request.URI, endpoint)
		}
		if interaction.Request.Method != strings.ToUpper(interaction.Request.Method) {
			t.Errorf("interaction %d method = %q, want the uppercase token the wire uses",
				i, interaction.Request.Method)
		}
		if interaction.Response.Status.Code == 0 {
			t.Errorf("interaction %d recorded no response for a run against a live server", i)
		}
		recorded, err := time.Parse(time.RFC1123, interaction.RecordedAt)
		if err != nil {
			t.Fatalf("interaction %d recorded_at %q does not parse as an HTTP-date: %v",
				i, interaction.RecordedAt, err)
		}
		if time.Since(recorded) > time.Minute || time.Since(recorded) < -time.Minute {
			t.Errorf("interaction %d was stamped %s, which is not when this run happened",
				i, interaction.RecordedAt)
		}
	}
}

// TestARecordedFileNeverCarriesTheCredentialTheRunWasGiven is the check this
// whole feature has to earn. A cassette is committed to a repository and
// replayed later, so a credential that reaches one outlives every terminal log
// it could have leaked into instead — and this goes through the binary because
// the redaction policy is set from a flag, which is the part a package test
// cannot see.
func TestARecordedFileNeverCarriesTheCredentialTheRunWasGiven(t *testing.T) {
	binary := build(t)
	endpoint, description := serve(t, "widgets")

	const credential = "e2e-bearer-do-not-record"
	directory := t.TempDir()

	for _, format := range []struct{ reporter, file string }{
		{"har", "run.har"},
		{"vcr", "run.yml"},
	} {
		t.Run(format.reporter, func(t *testing.T) {
			path := filepath.Join(directory, format.file)
			output, code := runCommand(t, binary, "run",
				"--endpoint", endpoint, "--no-color",
				"--header", "Authorization: Bearer "+credential,
				"--reporter", format.reporter, "--output", path, description)
			if code != 0 {
				t.Fatalf("exit = %d, want 0\n%s", code, output)
			}

			recorded, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("no recording was written: %v", err)
			}
			if strings.Contains(string(recorded), credential) {
				t.Errorf("the recording carries the credential:\n%s", recorded)
			}
			// Absent is not enough: the header has to still be there, marked, or
			// the reader cannot tell a redacted credential from a request that
			// was sent without one.
			if !strings.Contains(string(recorded), "Authorization") {
				t.Errorf("the credential header vanished rather than being redacted:\n%s", recorded)
			}
			if !strings.Contains(string(recorded), "<redacted>") {
				t.Errorf("the recording shows no redaction marker:\n%s", recorded)
			}
		})
	}
}
