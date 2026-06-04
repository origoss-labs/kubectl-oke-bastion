// Package ociconfig parses an OCI CLI config file (~/.oci/config) far enough to
// drive `init`'s profile picker: the ordered section names and each section's
// auth method. It is deliberately *not* a general INI library — it reads only
// what init needs (section order + the one key that distinguishes api_key from
// security_token) so the rest of the SDK config parsing stays the SDK's job.
//
// The OCI config format is INI-like: `[SECTION]` headers introduce a profile,
// `key = value` lines set entries, `#` and `;` introduce comments, and a leading
// header-less block is the
// implicit DEFAULT profile. A section is an api_key profile when it carries a
// `key_file` and a security_token profile when it carries a
// `security_token_file`; the latter wins because session-token profiles written
// by `oci session authenticate` carry both.
package ociconfig

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

// Section is one OCI config profile: its name and the auth method detected from
// its entries. Method reuses ociauth's string values so the detected method
// agrees with the rest of the system without translation.
type Section struct {
	Name   string
	Method ociauth.Method
}

// implicitDefault is the name OCI gives the leading header-less block.
const implicitDefault = "DEFAULT"

// ParseFile reads the OCI config at path and returns its sections in written
// order. A missing or unreadable file is reported as a wrapped error (callers
// can errors.Is it against os.ErrNotExist), never a panic.
func ParseFile(path string) ([]Section, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening OCI config %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	return Parse(f)
}

// Parse reads OCI config lines from r and returns its sections in written
// order. Taking an io.Reader (rather than a fixed path) keeps the parser
// testable over fixtures. A line that is neither blank, a comment, a header,
// nor a key=value entry, an unterminated header, or a section that resolves to
// no auth method is reported as a wrapped error.
func Parse(r io.Reader) ([]Section, error) {
	// cur accumulates the in-progress section; it is appended once the next
	// header (or EOF) is reached. The leading block, if it has any entries,
	// becomes the implicit DEFAULT section.
	var (
		sections []Section
		cur      *Section
	)

	flush := func() error {
		if cur == nil {
			return nil
		}
		if cur.Method == "" {
			return fmt.Errorf("OCI config section %q has neither key_file nor security_token_file", cur.Name)
		}
		sections = append(sections, *cur)
		return nil
	}

	sc := bufio.NewScanner(r)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("OCI config line %d: malformed section header %q", lineNo, line)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			name := strings.TrimSpace(line[1 : len(line)-1])
			if name == "" {
				return nil, fmt.Errorf("OCI config line %d: empty section header %q", lineNo, line)
			}
			cur = &Section{Name: name}
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("OCI config line %d: not a section header or key=value: %q", lineNo, line)
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		// A key=value line before any header belongs to the implicit DEFAULT
		// section; materialize it lazily on first entry.
		if cur == nil {
			cur = &Section{Name: implicitDefault}
		}
		switch key {
		case "security_token_file":
			if val != "" {
				cur.Method = ociauth.SecurityToken
			}
		case "key_file":
			// security_token wins if already set: session-token profiles carry
			// both files, and the security_token_file is the operative one.
			if val != "" && cur.Method == "" {
				cur.Method = ociauth.APIKey
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading OCI config: %w", err)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return sections, nil
}
