package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

const maxRemoteDescriptionBytes = 32 << 20

// Description fetches use their own client: the API's credential and transport
// settings must not travel to the document host. The same bytes then enter the
// ordinary parser/compiler whether supplied by a server or saved on disk.
var descriptionHTTPClient = &http.Client{Timeout: 30 * time.Second}

func remoteDescription(location string) bool {
	lower := strings.ToLower(location)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

func readDescription(location string) ([]byte, error) {
	return readDescriptionWithClient(location, descriptionHTTPClient)
}

func readDescriptionWithClient(location string, client *http.Client) ([]byte, error) {
	if !remoteDescription(location) {
		return os.ReadFile(location)
	}
	request, err := http.NewRequest(http.MethodGet, location, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid description URL: %w", err)
	}
	request.Header.Set("Accept", "application/vnd.oai.openapi+json, application/json, application/yaml, text/yaml, */*;q=0.1")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetching the API description: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching the API description: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxRemoteDescriptionBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading the remote API description: %w", err)
	}
	if len(body) > maxRemoteDescriptionBytes {
		return nil, fmt.Errorf("remote API description exceeds the %d-byte limit", maxRemoteDescriptionBytes)
	}
	return body, nil
}

// A URL query is not part of a filename: /openapi.json?revision=x and a saved
// openapi.json have the same compiler origin unless --filename overrides it.
func descriptionFilename(location string) string {
	if remoteDescription(location) {
		if parsed, err := url.Parse(location); err == nil {
			return path.Base(parsed.Path)
		}
	}
	return filepath.Base(location)
}
