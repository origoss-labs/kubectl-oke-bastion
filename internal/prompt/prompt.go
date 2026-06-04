// Package prompt is the thin interactive-selection glue for init: print a
// numbered list, read one line, validate the choice. It takes an io.Reader and
// io.Writer rather than binding to os.Stdin/Stdout so it stays testable and the
// command layer decides where I/O goes.
package prompt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Select prints title followed by a 1-based numbered list of options to w,
// reads a single line from r, and returns the 0-based index of the chosen
// option. A non-numeric, out-of-range, or empty/EOF response is a wrapped error
// rather than a re-prompt: the command layer (or the operator) decides whether
// to retry. It errors if options is empty.
func Select(r io.Reader, w io.Writer, title string, options []string) (int, error) {
	if len(options) == 0 {
		return 0, fmt.Errorf("nothing to choose from")
	}
	if _, err := fmt.Fprintln(w, title); err != nil {
		return 0, err
	}
	for i, opt := range options {
		if _, err := fmt.Fprintf(w, "  %d) %s\n", i+1, opt); err != nil {
			return 0, err
		}
	}
	if _, err := fmt.Fprintf(w, "Enter a number [1-%d]: ", len(options)); err != nil {
		return 0, err
	}

	line, err := bufio.NewReader(r).ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		return 0, fmt.Errorf("no selection made")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, fmt.Errorf("reading selection: %w", err)
	}
	choice, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", strings.TrimSpace(line))
	}
	if choice < 1 || choice > len(options) {
		return 0, fmt.Errorf("selection %d is out of range [1-%d]", choice, len(options))
	}
	return choice - 1, nil
}
