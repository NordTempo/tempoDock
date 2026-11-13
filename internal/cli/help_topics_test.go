package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// helpTopics is the web UI's help content (features/help, ADR 0076's
// Help): every "Do" row's `cli` is checked against the real commands.
const helpTopics = "../../web/src/features/help/topics.ts"

// The help's commands are real: each names an existing command, its
// flags exist, and its arguments fit. A renamed command or flag fails
// here, not in front of a reader.
func TestHelpTopicCommands(t *testing.T) {
	src, err := os.ReadFile(helpTopics)
	if err != nil {
		t.Fatal(err)
	}
	// A command is a "string", or a `template` where it holds ${{ }}.
	clis := regexp.MustCompile(`cli:\s*(?:"([^"]*)"|`+"`([^`]*)`"+`)`).FindAllStringSubmatch(string(src), -1)
	if len(clis) < 60 {
		t.Fatalf("found %d commands in %s; did the format change?", len(clis), helpTopics)
	}
	fill := strings.NewReplacer("{service}", "web", "{env}", "production", "{project}", "shop")
	for _, m := range clis {
		cli := m[1] + strings.ReplaceAll(m[2], `\$`, "$")
		if err := checkCommandLine(fill.Replace(cli)); err != "" {
			t.Errorf("%s: %s", cli, err)
		}
	}
}

// checkCommandLine parses line ("dock env set K=v -s web") against a
// fresh command tree and says what is wrong with it, or "".
func checkCommandLine(line string) string {
	words := shellWords(line)
	if len(words) == 0 || words[0] != "dock" {
		return "does not start with dock"
	}
	root := newRoot(&globals{})
	cmd, rest, err := root.Find(words[1:])
	if err != nil {
		return err.Error()
	}
	if err := cmd.ParseFlags(rest); err != nil {
		return err.Error()
	}
	args := cmd.Flags().Args()
	if !cmd.Runnable() {
		return cmd.CommandPath() + " only groups commands"
	}
	if cmd.HasSubCommands() && len(args) > 0 && cmd.Args == nil {
		return "unknown command " + args[0] + " under " + cmd.CommandPath()
	}
	if err := cmd.ValidateArgs(args); err != nil {
		return err.Error()
	}
	return ""
}

// shellWords splits a command line like a shell would for the help's
// examples: on spaces, keeping single- or double-quoted text together.
func shellWords(line string) []string {
	var words []string
	var word strings.Builder
	var quote rune
	in := false
	for _, r := range line {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, in = r, true
		case r == ' ':
			if in {
				words = append(words, word.String())
				word.Reset()
				in = false
			}
		default:
			word.WriteRune(r)
			in = true
		}
	}
	if in {
		words = append(words, word.String())
	}
	return words
}
