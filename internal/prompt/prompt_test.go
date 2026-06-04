package prompt

import (
	"strings"
	"testing"
)

func TestSelect_PicksByNumber(t *testing.T) {
	in := strings.NewReader("2\n")
	var out strings.Builder
	i, err := Select(in, &out, "Pick a profile:", []string{"DEFAULT", "TOKEN", "PROD"})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if i != 1 { // 1-based prompt "2" → 0-based index 1
		t.Errorf("index = %d, want 1", i)
	}
	// The list must be printed numbered so the operator knows what "2" means.
	if !strings.Contains(out.String(), "2) TOKEN") {
		t.Errorf("output %q does not show the numbered list", out.String())
	}
}

func TestSelect_RejectsOutOfRangeAndNonNumeric(t *testing.T) {
	cases := map[string]string{
		"zero":        "0\n",
		"too large":   "9\n",
		"non-numeric": "abc\n",
		"empty / EOF": "",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Select(strings.NewReader(input), &strings.Builder{}, "Pick:", []string{"A", "B"})
			if err == nil {
				t.Fatalf("expected an error for input %q, got nil", input)
			}
		})
	}
}

func TestSelect_EmptyOptionsErrors(t *testing.T) {
	if _, err := Select(strings.NewReader("1\n"), &strings.Builder{}, "Pick:", nil); err == nil {
		t.Fatal("expected an error when there are no options, got nil")
	}
}
