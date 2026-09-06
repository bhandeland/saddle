package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/brandon/saddle/internal/auth"
	"github.com/brandon/saddle/internal/doctor"
	"github.com/brandon/saddle/internal/render"
	"github.com/brandon/saddle/internal/runtime"
	"github.com/brandon/saddle/internal/session"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx := context.Background()
	var err error
	switch os.Args[1] {
	case "up":
		err = cmdUp(ctx, os.Args[2:])
	case "ls":
		err = cmdLs(ctx)
	case "down":
		err = cmdDown(ctx, os.Args[2:])
	case "doctor":
		err = cmdDoctor(ctx)
	case "auth":
		err = cmdAuth()
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "saddle:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, strings.TrimSpace(`
usage: saddle <command>

  up [path]        create a session and attach
  ls               list sessions
  down <name>      tear down a session
  doctor           check the local setup
  auth             store the container's Claude token
  version          print the version
`))
}

func cmdUp(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("up", flag.ExitOnError)
	name := fs.String("name", "", "session name (default: generated)")
	prof := fs.String("profile", "", "profile name (default: detected)")
	safe := fs.Bool("safe", false, "keep permission prompting enabled")
	noNet := fs.Bool("no-net", false, "deny all egress")
	openNet := fs.Bool("open-net", false, "disable egress filtering entirely")
	renderer := fs.String("render", "", "terminal|cmux (default: auto)")
	var allow multiFlag
	fs.Var(&allow, "allow", "additional allowed host (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	repo := "."
	if fs.NArg() > 0 {
		repo = fs.Arg(0)
	}
	_, err := session.Up(ctx, session.UpOptions{
		Repo: repo, Name: *name, ProfileName: *prof,
		Safe: *safe, NoNet: *noNet, OpenNet: *openNet,
		ExtraAllow: allow, Render: render.Mode(*renderer),
	})
	return err
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func cmdLs(ctx context.Context) error {
	states, err := session.List()
	if err != nil {
		return err
	}
	// Reconcile against reality: a container may have died behind saddle's
	// back, leaving a state file that still says running.
	if alive, err := runtime.Running(ctx); err == nil {
		states = session.Reconcile(states, alive)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPROFILE\tSTATUS\tWORKTREE")
	for _, s := range states {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", s.Name, s.Profile, s.Status, s.Worktree)
	}
	return w.Flush()
}

func cmdDown(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("down", flag.ExitOnError)
	force := fs.Bool("force", false, "tear down even with uncommitted work")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: saddle down <name> [--force]")
	}
	return session.Down(ctx, fs.Arg(0), *force)
}

func cmdDoctor(ctx context.Context) error {
	bad := 0
	for _, r := range doctor.Run(ctx) {
		mark := "ok  "
		if !r.OK {
			mark, bad = "FAIL", bad+1
		}
		fmt.Printf("%s  %s\n", mark, r.Name)
		if !r.OK {
			fmt.Printf("      %s\n      fix: %s\n", r.Detail, r.Fix)
		}
	}
	if bad > 0 {
		return fmt.Errorf("%d checks failed", bad)
	}
	return nil
}

func cmdAuth() error {
	fmt.Print("Paste a Claude Code OAuth token (from `claude setup-token`): ")
	var tok string
	if _, err := fmt.Scanln(&tok); err != nil {
		return err
	}
	if strings.TrimSpace(tok) == "" {
		return fmt.Errorf("no token entered")
	}
	return auth.Store(strings.TrimSpace(tok))
}
