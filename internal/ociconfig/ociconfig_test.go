package ociconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/origoss-labs/kubectl-oke-bastion/internal/ociauth"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []Section
		wantErr bool
	}{
		{
			// The leading, header-less block is OCI's implicit DEFAULT section;
			// it must appear first and carry the api_key method when keyed off a
			// key_file entry.
			name: "implicit DEFAULT then named sections in order",
			raw: `user=ocid1.user.oc1..a
fingerprint=aa:bb
key_file=~/.oci/oci_api_key.pem
tenancy=ocid1.tenancy.oc1..t
region=eu-frankfurt-1

[TOKEN]
security_token_file=~/.oci/sessions/tok/token
key_file=~/.oci/sessions/tok/oci_api_key.pem
region=us-ashburn-1

[KEYONLY]
key_file=~/.oci/other.pem
`,
			want: []Section{
				{Name: "DEFAULT", Method: ociauth.APIKey},
				// security_token_file present → security_token wins even though a
				// key_file is also present (session-token profiles carry both).
				{Name: "TOKEN", Method: ociauth.SecurityToken},
				{Name: "KEYONLY", Method: ociauth.APIKey},
			},
		},
		{
			name: "named-only sections preserve written order",
			raw: `[BETA]
key_file=/a.pem

[ALPHA]
security_token_file=/s/token
`,
			want: []Section{
				{Name: "BETA", Method: ociauth.APIKey},
				{Name: "ALPHA", Method: ociauth.SecurityToken},
			},
		},
		{
			name: "blank lines and comments and spaced equals are tolerated",
			raw: `# a comment
[PROD]
  key_file = /p.pem
# trailing comment
`,
			want: []Section{
				{Name: "PROD", Method: ociauth.APIKey},
			},
		},
		{
			// The OCI/INI dialect treats both `#` and `;` as comment leaders; a
			// `;`-prefixed line must be skipped, not rejected as garbled.
			name: "semicolon comments are tolerated",
			raw: `; a comment
[PROD]
key_file=/p.pem
; trailing comment
`,
			want: []Section{
				{Name: "PROD", Method: ociauth.APIKey},
			},
		},
		{
			// security_token must win regardless of which file line appears first,
			// so a key_file-first session profile still resolves to security_token.
			name: "security_token wins with key_file listed first",
			raw:  "[TOK]\nkey_file=/k.pem\nsecurity_token_file=/s/token\n",
			want: []Section{
				{Name: "TOK", Method: ociauth.SecurityToken},
			},
		},
		{
			name:    "empty section header errors",
			raw:     "[]\nkey_file=/x.pem\n",
			wantErr: true,
		},
		{
			name:    "section with neither key_file nor security_token_file errors",
			raw:     "[BROKEN]\nregion=us-ashburn-1\n",
			wantErr: true,
		},
		{
			name:    "garbled line outside any section errors",
			raw:     "this-is-not-a-key-value-line\n[X]\nkey_file=/x.pem\n",
			wantErr: true,
		},
		{
			name:    "malformed header errors",
			raw:     "[UNTERMINATED\nkey_file=/x.pem\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(strings.NewReader(tc.raw))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil (sections=%+v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse returned error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d sections %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("section[%d] = %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}

func TestParseFile_Missing(t *testing.T) {
	_, err := ParseFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a missing config file, got nil")
	}
	// A missing file must be a clean wrapped error, not a panic; the wrapped
	// cause should still be recognizable as a not-exist.
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("error %v does not wrap os.ErrNotExist", err)
	}
}

func TestParseFile_ReadsFixture(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	const raw = `[DEV]
key_file=/d.pem

[STAGE]
security_token_file=/s/token
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	want := []Section{
		{Name: "DEV", Method: ociauth.APIKey},
		{Name: "STAGE", Method: ociauth.SecurityToken},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("section[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
