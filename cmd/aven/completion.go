package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// subcommands are completed after the command name. Keep in sync with the command implementations.
var subcommands = map[string][]string{
	"environment": {"list", "new", "use", "delete", "sync", "restrict", "unrestrict"},
	"service":     {"list", "use"},
	"tokens":      {"list", "create", "revoke"},
	"config":      {"import", "pull", "plan", "preview", "apply", "partials"},
	"import":      {"dokploy"},
	"github":      {"status", "connect", "disconnect", "repos", "webhook"},
	"dev":         {"up", "down", "clean", "configure"},
	"sandbox":     {"create", "fork", "list", "exec", "ssh", "file", "checkpoint", "destroy"},
	"domain":      {"add", "list", "status", "port", "verify", "rename", "rm"},
	"tcp-proxy":   {"status", "enable", "port", "disable"},
	"cdn":         {"status", "enable", "disable", "update", "purge"},
	"waf":         {"status", "update", "under-attack"},
	"edge":        {"rules"},
	"ha":          {"status", "convert", "scale", "switchover", "auto-failover", "revert"},
	"pitr":        {"status", "enable", "disable", "base", "restore", "delete-archive"},
	"mysql":       {"pitr"},
	"postgres":    {"pooling", "upgrade", "pitr"},
	"flag":        {"list", "set", "unset", "delete"},
	"trace":       {"on", "off", "status", "list", "get"},
	"templates":   {"list", "show", "deploy", "publish", "export", "export-env"},
	"mcp":         {"install"},
	"completion":  {"bash", "zsh", "fish", "powershell"},
}

// commonFlags are offered for every command (the command decides which it accepts).
var commonFlags = []string{"--service", "--environment", "--json", "--help"}

func cmdCompletion(args []string) {
	if len(args) != 1 {
		dieUsage("usage: aven completion bash|zsh|fish|powershell\n\nexamples:\n  source <(aven completion bash)\n  aven completion zsh > \"${fpath[1]}/_aven\"\n  aven completion fish > ~/.config/fish/completions/aven.fish")
	}
	if err := writeCompletion(os.Stdout, args[0]); err != nil {
		dieUsage("%v", err)
	}
}

// completionData returns the top-level words and "cmd sub1 sub2 ..." lines (aliases share the subcommands).
func completionData() (top []string, subs map[string][]string) {
	subs = map[string][]string{}
	for _, c := range commands {
		key := c.names[0]
		if c.helpAs != "" && subcommands[key] == nil {
			key = c.helpAs
		}
		for _, n := range c.names {
			if s := subcommands[key]; s != nil {
				subs[n] = s
			}
		}
		top = append(top, c.names...)
	}
	top = append(top, "help")
	subs["help"] = commandNames(false)
	sort.Strings(top)
	return top, subs
}

func writeCompletion(w io.Writer, shell string) error {
	top, subs := completionData()
	names := make([]string, 0, len(subs))
	for k := range subs {
		names = append(names, k)
	}
	sort.Strings(names)
	switch shell {
	case "bash":
		var cases strings.Builder
		for _, n := range names {
			fmt.Fprintf(&cases, "    %s) words=%q ;;\n", n, strings.Join(subs[n], " "))
		}
		fmt.Fprintf(w, `# bash completion for aven. Load with: source <(aven completion bash)
_aven() {
  local cur=${COMP_WORDS[COMP_CWORD]} words=""
  if [[ $cur == -* ]]; then
    COMPREPLY=($(compgen -W %q -- "$cur")); return
  fi
  if (( COMP_CWORD == 1 )); then
    COMPREPLY=($(compgen -W %q -- "$cur")); return
  fi
  if (( COMP_CWORD == 2 )); then
    case ${COMP_WORDS[1]} in
%s    esac
    if [[ -n $words ]]; then COMPREPLY=($(compgen -W "$words" -- "$cur")); return; fi
  fi
  COMPREPLY=($(compgen -f -- "$cur"))
}
complete -o default -F _aven aven
`, strings.Join(commonFlags, " "), strings.Join(top, " "), cases.String())
	case "zsh":
		var cases strings.Builder
		for _, n := range names {
			fmt.Fprintf(&cases, "        %s) compadd -- %s ;;\n", n, strings.Join(subs[n], " "))
		}
		fmt.Fprintf(w, `#compdef aven
# zsh completion for aven. Save as _aven in a directory of $fpath, or: source <(aven completion zsh)
_aven() {
  if [[ $words[CURRENT] == -* ]]; then
    compadd -- %s; return
  fi
  case $CURRENT in
    2) compadd -- %s ;;
    3)
      case $words[2] in
%s        *) _files ;;
      esac ;;
    *) _files ;;
  esac
}
if [[ $zsh_eval_context[-1] == loadautofunc ]]; then _aven "$@"; else compdef _aven aven; fi
`, strings.Join(commonFlags, " "), strings.Join(top, " "), cases.String())
	case "fish":
		fmt.Fprintln(w, "# fish completion for aven. Save as ~/.config/fish/completions/aven.fish")
		fmt.Fprintln(w, "complete -c aven -f")
		fmt.Fprintf(w, "complete -c aven -n __fish_use_subcommand -a %q\n", strings.Join(top, " "))
		for _, n := range names {
			fmt.Fprintf(w, "complete -c aven -n '__fish_seen_subcommand_from %s; and test (count (commandline -opc)) -eq 2' -a %q\n", n, strings.Join(subs[n], " "))
		}
		for _, f := range commonFlags {
			fmt.Fprintf(w, "complete -c aven -l %s\n", strings.TrimPrefix(f, "--"))
		}
	case "powershell", "pwsh":
		var cases strings.Builder
		for _, n := range names {
			fmt.Fprintf(&cases, "    '%s' { $candidates = @('%s') }\n", n, strings.Join(subs[n], "','"))
		}
		fmt.Fprintf(w, `# PowerShell completion for aven. Load with: aven completion powershell | Out-String | Invoke-Expression
Register-ArgumentCompleter -Native -CommandName aven, aven.exe -ScriptBlock {
  param($wordToComplete, $commandAst, $cursorPosition)
  $words = @($commandAst.CommandElements | ForEach-Object { $_.ToString() })
  $position = $words.Count
  if ($wordToComplete -ne '') { $position = $words.Count - 1 }
  $candidates = @()
  if ($wordToComplete -like '-*') { $candidates = @('%s') }
  elseif ($position -le 1) { $candidates = @('%s') }
  elseif ($position -eq 2) {
    switch ($words[1]) {
%s    }
  }
  $candidates | Where-Object { $_ -like "$wordToComplete*" } | ForEach-Object {
    [System.Management.Automation.CompletionResult]::new($_, $_, 'ParameterValue', $_)
  }
}
`, strings.Join(commonFlags, "','"), strings.Join(top, "','"), cases.String())
	default:
		return fmt.Errorf("unknown shell %q (bash, zsh, fish or powershell)", shell)
	}
	return nil
}
