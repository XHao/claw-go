package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AppendUserFacts appends a list of observed user facts to the dynamic profile
// file at path. Each fact is written as a timestamped bullet point.
// The file is created if it does not exist. If facts is empty, no file is written.
func AppendUserFacts(path string, facts []string) error {
	// Filter out empty/whitespace-only facts before touching the filesystem.
	var cleaned []string
	for _, f := range facts {
		if s := strings.TrimSpace(f); s != "" {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("userprofile: mkdir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("userprofile: open: %w", err)
	}
	defer f.Close()

	date := time.Now().UTC().Format("2006-01-02")
	var sb strings.Builder
	for _, fact := range cleaned {
		sb.WriteString(fmt.Sprintf("- [%s] %s\n", date, fact))
	}
	_, err = f.WriteString(sb.String())
	return err
}

// LoadDynamicProfile reads the dynamic user profile file and returns its
// contents. Returns ("", nil) if the file does not exist.
func LoadDynamicProfile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("userprofile: read: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
