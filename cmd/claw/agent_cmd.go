package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/XHao/claw-go/agentdef"
	"github.com/XHao/claw-go/dirs"
)

// runAgentCmd dispatches claw agent <subcommand> [args].
func runAgentCmd(args []string) {
	if len(args) == 0 {
		printAgentUsage()
		os.Exit(1)
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		runAgentList()
	case "new":
		runAgentNew(rest)
	case "default":
		runAgentDefault(rest)
	default:
		fmt.Fprintf(os.Stderr, "unknown agent subcommand: %q\n", sub)
		printAgentUsage()
		os.Exit(1)
	}
}

func printAgentUsage() {
	fmt.Fprintf(os.Stderr, `claw agent – manage Persona Agents

Usage:
  claw agent list                     List all available embedded templates
  claw agent new <type> [--name=<n>]  Create agent from template
  claw agent default [<name>]         Get or set the default agent

`)
}

// runAgentList prints a table of all embedded templates.
func runAgentList() {
	metas, err := agentdef.ListTemplates(agentTemplateFS)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 3, ' ', 0)
	fmt.Fprintln(w, "TYPE\tNAME\tDESCRIPTION")
	for _, m := range metas {
		fmt.Fprintf(w, "%s\t%s\t%s\n", m.TypeKey, m.Name, m.Description)
	}
	w.Flush()
}

// runAgentNew creates an agent directory from an embedded template.
func runAgentNew(args []string) {
	fs := flag.NewFlagSet("agent new", flag.ExitOnError)
	nameFlag := fs.String("name", "", "custom agent name (defaults to type key)")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Fprintf(os.Stderr, "usage: claw agent new <type> [--name=<name>]\n")
		os.Exit(1)
	}
	typeKey := fs.Arg(0)
	name := strings.TrimSpace(*nameFlag)
	if name == "" {
		name = typeKey
	}

	if name == typeKey {
		if err := agentdef.InstallTemplate(agentTemplateFS, typeKey, dirs.AgentsDir()); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	} else {
		tmp, err := os.MkdirTemp("", "claw-agent-*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: create temp dir: %v\n", err)
			os.Exit(1)
		}
		defer os.RemoveAll(tmp)
		if err := agentdef.InstallTemplate(agentTemplateFS, typeKey, tmp); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		src := filepath.Join(tmp, typeKey)
		dst := dirs.AgentDir(name)
		if err := os.Rename(src, dst); err != nil {
			fmt.Fprintf(os.Stderr, "error: rename agent dir: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("Agent created: %s\n", dirs.AgentDir(name))
}

// runAgentDefault gets or sets the default agent in agent-state.json.
func runAgentDefault(args []string) {
	if len(args) == 0 {
		state, err := agentdef.LoadState(dirs.AgentStateFile())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if state.Default == "" {
			fmt.Println("(no default agent set)")
		} else {
			fmt.Println(state.Default)
		}
		return
	}

	name := strings.TrimSpace(args[0])
	if _, err := os.Stat(dirs.AgentDir(name)); err != nil {
		fmt.Fprintf(os.Stderr, "error: agent %q not found at %s\n", name, dirs.AgentDir(name))
		os.Exit(1)
	}
	if err := agentdef.SaveState(dirs.AgentStateFile(), agentdef.AgentState{Default: name}); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Default agent set to: %s\n", name)
}
