package runner

import (
	"encoding/pem"
	"os"
)

func writePEM(path string, der []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func writeFile(path, content string) {
	_ = os.WriteFile(path, []byte(content), 0o600)
}
