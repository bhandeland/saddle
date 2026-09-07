package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
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
	case "attach":
		err = cmdAttach(ctx, os.Args[2:])
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
  attach <name>    re-attach to an existing session
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
	// back, leaving a state file that still says running. If we can't check,
	// say so rather than silently showing possibly-stale statuses.
	alive, err := runtime.Running(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "saddle: could not reconcile against running containers, statuses may be stale: %v\n", err)
	} else {
		states = session.Reconcile(states, alive)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "NAME\tPROFILE\tSTATUS\tEGRESS\tWORKTREE")
	for _, s := range states {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, s.Profile, s.Status, s.EgressLabel(), s.Worktree)
	}
	return w.Flush()
}

// cmdAttach re-attaches to a session that already exists. In cmux mode a
// workspace closed by accident would otherwise leave a live session with no
// supported way back in. It creates nothing: the container, network, and
// egress proxy are whatever `saddle up` left behind.
func cmdAttach(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("attach", flag.ExitOnError)
	renderer := fs.String("render", "", "terminal|cmux (default: auto)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: saddle attach <name> [--render terminal|cmux]")
	}
	st, err := session.Load(fs.Arg(0))
	if err != nil {
		return err
	}
	mode := render.Mode(*renderer)
	if mode == "" {
		mode = render.Auto()
	}
	return render.Attach(ctx, mode, st.Name, st.Worktree, runtime.AttachArgv(st.Container))
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
	// fmt.Scanln would echo the token to the terminal (and to any scrollback
	// or screen-sharing session) and would stop at the first space. Read the
	// whole line with echo suppressed instead.
	restore, err := suppressEcho()
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nsaddle: could not disable terminal echo (%v); the token will be visible as you paste it\n", err)
	}
	tok, err := bufio.NewReader(os.Stdin).ReadString('\n')
	restore()
	fmt.Println()
	// io.EOF with content is a paste without a trailing newline, which is
	// fine; only an error with nothing to show for it is fatal.
	if err != nil && (!errors.Is(err, io.EOF) || tok == "") {
		return err
	}
	if strings.TrimSpace(tok) == "" {
		return fmt.Errorf("no token entered")
	}
	return auth.Store(strings.TrimSpace(tok))
}

// suppressEcho turns off terminal echo and returns a function that turns it
// back on. It shells out to stty rather than taking a dependency for this
// one call. If stty is unavailable the returned restore is a no-op and the
// error is returned: the caller warns and reads anyway, because refusing to
// accept a token at all would be worse than an echoed one the operator was
// told about.
func suppressEcho() (restore func(), err error) {
	stty := func(arg string) error {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = os.Stdin
		return cmd.Run()
	}
	if err := stty("-echo"); err != nil {
		return func() {}, err
	}
	return func() { _ = stty("echo") }, nil
}
