package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestEveryCommandHasHelp(t *testing.T) {
	for _, c := range commands {
		key := c.names[0]
		if c.helpAs != "" {
			key = c.helpAs
		}
		if len(helpEntries(key)) == 0 {
			t.Errorf("%s: no entry in helpText", c.names[0])
		}
		if c.run == nil {
			t.Errorf("%s: no run function", c.names[0])
		}
	}
	for _, k := range sortedKeys(examples) {
		if lookupCommand(k) == nil {
			t.Errorf("examples for unknown command %q", k)
		}
	}
	for _, k := range sortedKeys(subcommands) {
		if c := lookupCommand(k); c == nil || c.names[0] != k {
			t.Errorf("subcommands key %q is not a canonical command", k)
		}
	}
	for k := range targetCommands {
		if lookupCommand(k) == nil {
			t.Errorf("targetCommands: unknown %q", k)
		}
	}
}

// TestCommandAliases keeps every name the CLI accepted before the command table.
func TestCommandAliases(t *testing.T) {
	for _, n := range strings.Fields(`login link up logs api vars variables variable config import github gh status init
		environment env service add run dev develop shell ssh connect netdiag redeploy down restart scale flag flags pitr
		mysql cdn waf edge ha domain domains tcp-proxy tcpproxy networking net postgres pg open whoami tokens token usage
		trace traces tracing templates template mcp agent sandbox sandboxes sbx logout version`) {
		if lookupCommand(n) == nil {
			t.Errorf("command %q is gone", n)
		}
	}
	seen := map[string]bool{}
	for _, n := range commandNames(true) {
		if seen[n] {
			t.Errorf("duplicate command name %q", n)
		}
		seen[n] = true
	}
}

func TestCommandHelp(t *testing.T) {
	var b bytes.Buffer
	printCommandHelp(&b, "logs")
	out := b.String()
	for _, want := range []string{"Usage:\n  aven logs [--build]", "aven logs --dns", "Examples:", "-s, --service"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	// a command's entries only: "logout" must not show up in "login"'s help
	b.Reset()
	printCommandHelp(&b, "login")
	if strings.Contains(b.String(), "logout") || !strings.Contains(b.String(), "KEEL_TOKEN uses Bearer") {
		t.Errorf("login help:\n%s", b.String())
	}
	b.Reset()
	printCommandHelp(&b, "vars")
	if !strings.Contains(b.String(), "aven variables [--set") {
		t.Errorf("vars help:\n%s", b.String())
	}
}

func TestCompletionScripts(t *testing.T) {
	for _, sh := range []string{"bash", "zsh", "fish", "powershell"} {
		var b bytes.Buffer
		if err := writeCompletion(&b, sh); err != nil {
			t.Fatalf("%s: %v", sh, err)
		}
		for _, want := range []string{"environment", "logs", "tcp-proxy"} {
			if !strings.Contains(b.String(), want) {
				t.Errorf("%s: missing %q", sh, want)
			}
		}
	}
	if err := writeCompletion(&bytes.Buffer{}, "tcsh"); err == nil {
		t.Error("unknown shell accepted")
	}
	_, subs := completionData()
	if strings.Join(subs["env"], " ") != strings.Join(subcommands["environment"], " ") {
		t.Errorf("alias env: %v", subs["env"])
	}
	if len(subs["help"]) < 40 {
		t.Errorf("help completes commands: %v", subs["help"])
	}
}
