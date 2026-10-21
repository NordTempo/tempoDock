// Package output holds the CLI's shared conventions: --json versus human
// output, tables, status glyphs, colors, prompts and exit codes.
//
// Rules every command follows:
//   - Results go to stdout; errors, warnings and progress go to stderr.
//   - With --json, stdout is exactly one JSON value and errors are JSON on
//     stderr, so scripts never parse human text.
//   - Color only when the stream is a terminal and NO_COLOR is unset.
//   - Prompt only when stdin is a terminal; otherwise missing input is a
//     usage error naming the flag to pass.
package output

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"
)

// Status glyphs. Plain characters, so they survive pipes and logs.
const (
	GlyphOK      = "✓"
	GlyphFail    = "✗"
	GlyphWarn    = "!"
	GlyphPending = "·"
)

// IO is what a command reads from and writes to, and how.
type IO struct {
	In       io.Reader
	Out, Err io.Writer
	// JSON selects machine-readable output (--json).
	JSON bool
	// Interactive means stdin is a terminal, so prompting is allowed.
	Interactive bool
	// OutColor and ErrColor enable ANSI colors on Out and Err.
	OutColor, ErrColor bool

	in *bufio.Reader // In, buffered once so prompts never lose read-ahead
}

// Std returns IO on the process's standard streams with terminal detection.
func Std() *IO {
	noColor := os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb"
	return &IO{
		In:          os.Stdin,
		Out:         os.Stdout,
		Err:         os.Stderr,
		Interactive: isTerminal(os.Stdin),
		OutColor:    !noColor && isTerminal(os.Stdout),
		ErrColor:    !noColor && isTerminal(os.Stderr),
	}
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// Print writes v as indented JSON in --json mode, otherwise runs human.
// v should be a struct with json tags or a slice of them: that shape is the
// command's documented, stable output.
func (o *IO) Print(v any, human func() error) error {
	if !o.JSON {
		return human()
	}
	enc := json.NewEncoder(o.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// Successf prints "✓ message" to Out. Human mode only: call it from the
// human func passed to Print.
func (o *IO) Successf(format string, a ...any) {
	line(o.Out, paint(o.OutColor, green, GlyphOK)+" "+fmt.Sprintf(format, a...))
}

// Warnf prints "! message" to Err. Silent with --json, where stderr carries
// only the JSON error object.
func (o *IO) Warnf(format string, a ...any) {
	if o.JSON {
		return
	}
	line(o.Err, paint(o.ErrColor, yellow, GlyphWarn)+" "+fmt.Sprintf(format, a...))
}

// Progressf prints "· message" to Err: a step of something in progress.
// Silent with --json.
func (o *IO) Progressf(format string, a ...any) {
	if o.JSON {
		return
	}
	line(o.Err, paint(o.ErrColor, faint, GlyphPending)+" "+fmt.Sprintf(format, a...))
}

// Failf prints "✗ message" to Err. Silent with --json.
func (o *IO) Failf(format string, a ...any) {
	if o.JSON {
		return
	}
	line(o.Err, paint(o.ErrColor, red, GlyphFail)+" "+fmt.Sprintf(format, a...))
}

// Detail prints output relayed from the server (such as build log lines)
// indented and faint on Err. Silent with --json.
func (o *IO) Detail(s string) {
	if o.JSON {
		return
	}
	line(o.Err, paint(o.ErrColor, faint, "  "+s))
}

// Fields writes aligned "key  value" lines to Out from alternating keys and
// values. Pairs with an empty value are left out.
func (o *IO) Fields(kv ...string) error {
	tw := tabwriter.NewWriter(o.Out, 0, 0, 2, ' ', 0)
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			line(tw, kv[i]+"\t"+cellSpace.Replace(kv[i+1]))
		}
	}
	return tw.Flush()
}

// Table writes aligned columns to Out. Cells must not contain color codes
// (they would break alignment); put status in a glyph or word instead.
type Table struct {
	tw *tabwriter.Writer
}

// Table starts a table with upper-case headers.
func (o *IO) Table(headers ...string) *Table {
	t := &Table{tw: tabwriter.NewWriter(o.Out, 0, 0, 2, ' ', 0)}
	upper := make([]string, len(headers))
	for i, h := range headers {
		upper[i] = strings.ToUpper(h)
	}
	t.Row(upper...)
	return t
}

// Rows starts a table without a header row.
func (o *IO) Rows() *Table {
	return &Table{tw: tabwriter.NewWriter(o.Out, 0, 0, 2, ' ', 0)}
}

var cellSpace = strings.NewReplacer("\t", " ", "\n", " ")

// Row adds one row. Tabs and newlines in cells are replaced by spaces, and
// trailing empty cells are dropped.
func (t *Table) Row(cells ...string) {
	for len(cells) > 1 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1] // no trailing padding
	}
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteByte('\t')
		}
		b.WriteString(cellSpace.Replace(c))
	}
	line(t.tw, b.String()) // tabwriter buffers; errors surface in Flush
}

// Flush writes the table out.
func (t *Table) Flush() error { return t.tw.Flush() }

// PrintError writes err to Err: "error: message" plus the hint in human
// mode, a JSON object in --json mode.
func (o *IO) PrintError(err error) {
	e, ok := errors.AsType[*Error](err)
	if !ok {
		e = &Error{Exit: ExitError, Err: err}
	}
	if o.JSON {
		code := e.Code
		if code == "" {
			code = e.Exit.String()
		}
		type body struct {
			Code     string `json:"code"`
			ExitCode int    `json:"exitCode"`
			Message  string `json:"message"`
			Hint     string `json:"hint,omitempty"`
		}
		_ = json.NewEncoder(o.Err).Encode(struct {
			Error body `json:"error"`
		}{body{code, int(e.Exit), err.Error(), e.Hint}})
		return
	}
	line(o.Err, paint(o.ErrColor, red, "error:")+" "+err.Error())
	if e.Hint != "" {
		line(o.Err, e.Hint)
	}
}

// Prompt asks for a line of input, or fails with a usage error naming flag
// when stdin isn't a terminal. An empty answer is also a usage error.
func (o *IO) Prompt(label, flag string) (string, error) {
	if !o.Interactive {
		return "", MissingFlag(flag)
	}
	s, err := o.readLine(label + ": ")
	if err != nil {
		return "", MissingFlag(flag)
	}
	return nonEmpty(s, flag)
}

func (o *IO) readLine(prompt string) (string, error) {
	if o.in == nil {
		o.in = bufio.NewReader(o.In)
	}
	_, _ = io.WriteString(o.Err, prompt)
	s, err := o.in.ReadString('\n')
	if err != nil && s == "" {
		return "", err
	}
	return strings.TrimSpace(s), nil
}

// Choose asks for one of options, by number or by name, or fails with a
// usage error naming flag when stdin isn't a terminal.
func (o *IO) Choose(label, flag string, options []string) (string, error) {
	if !o.Interactive {
		return "", MissingFlag(flag)
	}
	for i, opt := range options {
		_, _ = fmt.Fprintf(o.Err, "  %d) %s\n", i+1, opt)
	}
	s, err := o.Prompt(label, flag)
	if err != nil {
		return "", err
	}
	if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(options) {
		return options[n-1], nil
	}
	if slices.Contains(options, s) {
		return s, nil
	}
	return "", &Error{Exit: ExitUsage, Message: fmt.Sprintf("%q is not one of the choices", s), Hint: "Pass --" + flag + "."}
}

// Confirm asks a yes/no question that defaults to no. Without a terminal it
// fails with a usage error naming flag (the one that skips the question).
func (o *IO) Confirm(question, flag string) error {
	if !o.Interactive {
		return &Error{
			Exit:    ExitUsage,
			Message: "confirmation required: " + question,
			Hint:    "Pass --" + flag + " to confirm. The CLI only prompts when stdin is a terminal.",
		}
	}
	s, err := o.readLine(question + " [y/N] ")
	if err == nil && (strings.EqualFold(s, "y") || strings.EqualFold(s, "yes")) {
		return nil
	}
	return &Error{Exit: ExitError, Message: "cancelled; nothing was changed"}
}

// PromptSecret is Prompt without echoing the input.
func (o *IO) PromptSecret(label, flag string) (string, error) {
	f, ok := o.In.(*os.File)
	if !o.Interactive || !ok {
		return "", MissingFlag(flag)
	}
	_, _ = io.WriteString(o.Err, label+": ")
	b, err := term.ReadPassword(int(f.Fd()))
	line(o.Err, "")
	if err != nil {
		return "", err
	}
	return nonEmpty(strings.TrimSpace(string(b)), flag)
}

func nonEmpty(s, flag string) (string, error) {
	if s == "" {
		return "", MissingFlag(flag)
	}
	return s, nil
}

// MissingFlag is the usage error for input that wasn't given and can't be
// prompted for.
func MissingFlag(flag string) *Error {
	return &Error{
		Exit:    ExitUsage,
		Message: "missing --" + flag,
		Hint:    "Pass --" + flag + ". The CLI only prompts when stdin is a terminal.",
	}
}

// line writes s and a newline. Terminal output errors are not actionable,
// so they are dropped.
func line(w io.Writer, s string) { _, _ = io.WriteString(w, s+"\n") }

const (
	faint  = "2"
	red    = "31"
	green  = "32"
	yellow = "33"
)

func paint(on bool, color, s string) string {
	if !on {
		return s
	}
	return "\x1b[" + color + "m" + s + "\x1b[0m"
}
