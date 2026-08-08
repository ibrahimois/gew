package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	ucli "github.com/urfave/cli/v3"

	"gew/internal/version"
)

type loginOptions struct {
	Name, Provider, Token, AuthKind, Username, URL, RequestTimeout string
	Insecure                                                       bool
}

type cloneOptions struct {
	Repository, Directory, Branch string
	Backend                       WorkspaceBackendKind
}

type addOptions struct {
	Paths []string
	All   bool
}

type commitOptions struct {
	Message, AuthorName, AuthorEmail string
}

type mergeOptions struct {
	Abort, Continue bool
	Message         string
}

type migrateOptions struct {
	Target, AuthorName, AuthorEmail string
	DryRun                          bool
}

type releaseCreateOptions struct {
	Tag, Title, NotesFile string
	Assets                []string
	Draft, Prerelease     bool
	Resume                bool
}

func Run(args []string, output, errorOutput io.Writer) error {
	return RunContext(context.Background(), args, output, errorOutput)
}

func RunContext(ctx context.Context, args []string, output, errorOutput io.Writer) error {
	application := newCommand(app{out: output, errOut: errorOutput})
	return application.Run(ctx, append([]string{"gew"}, args...))
}

func newCommand(application app) *ucli.Command {
	var showVersion bool
	root := &ucli.Command{
		Name:                  "gew",
		Usage:                 "a small REST-only workspace client for hosted Git forges",
		Writer:                application.out,
		ErrWriter:             application.errOut,
		HideVersion:           true,
		EnableShellCompletion: true,
		ConfigureShellCompletionCommand: func(command *ucli.Command) {
			command.Hidden = false
		},
		UseShortOptionHandling: false,
		PrefixMatchCommands:    false,
		ExitErrHandler:         func(context.Context, *ucli.Command, error) {},
		Flags: []ucli.Flag{
			&ucli.BoolFlag{Name: "version", Aliases: []string{"v"}, Usage: "print the version", Destination: &showVersion},
		},
	}
	root.Action = func(ctx context.Context, command *ucli.Command) error {
		if showVersion {
			return printVersion(application.out)
		}
		if command.NArg() == 0 {
			return ucli.ShowRootCommandHelp(command)
		}
		return fmt.Errorf("unknown command %q; run 'gew help'", command.Args().First())
	}

	root.Commands = []*ucli.Command{
		loginCommand(application),
		doctorCommand(application),
		cloneCommand(application),
		statusCommand(application),
		addCommand(application),
		resetCommand(application),
		diffCommand(application),
		commitCommand(application),
		uncommitCommand(application),
		logCommand(application),
		pullCommand(application),
		mergeCommand(application),
		migrateCommand(application),
		pushCommand(application),
		releaseCommand(application),
		versionCommand(application),
	}
	applyUsagePolicy(root)
	return root
}

func applyUsagePolicy(command *ucli.Command) {
	command.OnUsageError = func(_ context.Context, _ *ucli.Command, err error, _ bool) error {
		return err
	}
	for _, child := range command.Commands {
		applyUsagePolicy(child)
	}
}

func printVersion(output io.Writer) error {
	_, err := fmt.Fprintf(output, "gew %s\n", version.Current)
	return err
}

func requireArity(command *ucli.Command, minimum, maximum int) error {
	count := command.NArg()
	if count < minimum || (maximum >= 0 && count > maximum) {
		if minimum == maximum {
			return fmt.Errorf("expected %d argument(s), got %d", minimum, count)
		}
		return fmt.Errorf("expected between %d and %d arguments, got %d", minimum, maximum, count)
	}
	return nil
}

func noArgsAction(action func(context.Context) error) ucli.ActionFunc {
	return func(ctx context.Context, command *ucli.Command) error {
		if err := requireArity(command, 0, 0); err != nil {
			return err
		}
		return action(ctx)
	}
}

func loginCommand(application app) *ucli.Command {
	options := loginOptions{Name: "default", Provider: string(ForgeGitea)}
	return &ucli.Command{
		Name:      "login",
		Usage:     "save and verify a forge login profile",
		ArgsUsage: "URL",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "provider", Value: string(ForgeGitea), Usage: "provider kind", Destination: &options.Provider},
			&ucli.StringFlag{Name: "name", Value: "default", Usage: "profile name", Destination: &options.Name},
			&ucli.StringFlag{Name: "token", Usage: "access token (reads GEW_TOKEN when omitted)", Sources: ucli.EnvVars("GEW_TOKEN"), Destination: &options.Token},
			&ucli.StringFlag{Name: "auth-kind", Usage: "authentication kind", Destination: &options.AuthKind},
			&ucli.StringFlag{Name: "username", Usage: "authentication username", Destination: &options.Username},
			&ucli.StringFlag{Name: "request-timeout", Usage: "per-request timeout (1s to 30m)", Sources: ucli.EnvVars("GEW_HTTP_TIMEOUT"), Destination: &options.RequestTimeout},
			&ucli.BoolFlag{Name: "insecure", Usage: "skip TLS verification", Destination: &options.Insecure},
		},
		Action: func(ctx context.Context, command *ucli.Command) error {
			if err := requireArity(command, 1, 1); err != nil {
				return err
			}
			options.URL = command.Args().First()
			return application.loginOperation(ctx, options)
		},
	}
}

func doctorCommand(application app) *ucli.Command {
	return &ucli.Command{Name: "doctor", Usage: "verify the active forge profile", Action: noArgsAction(application.doctorOperation)}
}

func cloneCommand(application app) *ucli.Command {
	options := cloneOptions{Backend: WorkspaceGew}
	return &ucli.Command{
		Name: "clone", Usage: "download a repository through its forge API", ArgsUsage: "OWNER/REPO [DIRECTORY]",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "branch", Usage: "branch to download", Destination: &options.Branch},
			&ucli.StringFlag{Name: "backend", Value: string(WorkspaceGew), Usage: "local workspace backend (gew or git)", Destination: (*string)(&options.Backend)},
		},
		Action: func(ctx context.Context, command *ucli.Command) error {
			if err := requireArity(command, 1, 2); err != nil {
				return err
			}
			options.Repository = command.Args().Get(0)
			options.Directory = command.Args().Get(1)
			return application.cloneOperation(ctx, options)
		},
	}
}

func statusCommand(application app) *ucli.Command {
	var asJSON bool
	return &ucli.Command{
		Name: "status", Usage: "show workspace status",
		Flags:  []ucli.Flag{&ucli.BoolFlag{Name: "json", Usage: "print machine-readable JSON", Destination: &asJSON}},
		Action: noArgsAction(func(ctx context.Context) error { return application.statusOperation(ctx, asJSON) }),
	}
}

func addCommand(application app) *ucli.Command {
	options := addOptions{}
	return &ucli.Command{
		Name: "add", Usage: "stage workspace changes", ArgsUsage: "PATH...",
		Flags: []ucli.Flag{&ucli.BoolFlag{Name: "all", Aliases: []string{"A"}, Usage: "stage all changes", Destination: &options.All}},
		Action: func(ctx context.Context, command *ucli.Command) error {
			options.Paths = command.Args().Slice()
			if !options.All && len(options.Paths) == 0 {
				return errors.New("at least one PATH is required unless --all is set")
			}
			return application.addOperation(ctx, options)
		},
	}
}

func resetCommand(application app) *ucli.Command {
	return &ucli.Command{
		Name: "reset", Usage: "unstage workspace changes", ArgsUsage: "[PATH...]",
		Action: func(ctx context.Context, command *ucli.Command) error {
			return application.resetOperation(ctx, command.Args().Slice())
		},
	}
}

func diffCommand(application app) *ucli.Command {
	var staged bool
	return &ucli.Command{
		Name: "diff", Usage: "show workspace differences",
		Flags:  []ucli.Flag{&ucli.BoolFlag{Name: "staged", Usage: "show staged changes", Destination: &staged}},
		Action: noArgsAction(func(ctx context.Context) error { return application.diffOperation(ctx, staged) }),
	}
}

func commitCommand(application app) *ucli.Command {
	options := commitOptions{}
	return &ucli.Command{
		Name: "commit", Usage: "record staged changes locally",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "message", Aliases: []string{"m"}, Usage: "commit message", Required: true, Destination: &options.Message, Validator: nonblank("message")},
			&ucli.StringFlag{Name: "author-name", Usage: "local Git author name", Destination: &options.AuthorName},
			&ucli.StringFlag{Name: "author-email", Usage: "local Git author email", Destination: &options.AuthorEmail},
		},
		Action: noArgsAction(func(ctx context.Context) error {
			options.Message = strings.TrimSpace(options.Message)
			return application.commitOperation(ctx, options)
		}),
	}
}

func uncommitCommand(application app) *ucli.Command {
	return &ucli.Command{
		Name: "uncommit", Usage: "undo the newest unpushed commit and restore its staged snapshot",
		Action: noArgsAction(application.uncommitOperation),
	}
}

func nonblank(name string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s must not be blank", name)
		}
		return nil
	}
}

func logCommand(application app) *ucli.Command {
	var oneline bool
	return &ucli.Command{
		Name: "log", Usage: "show local commit history",
		Flags:  []ucli.Flag{&ucli.BoolFlag{Name: "oneline", Usage: "one commit per line", Destination: &oneline}},
		Action: noArgsAction(func(ctx context.Context) error { return application.logOperation(ctx, oneline) }),
	}
}

func pullCommand(application app) *ucli.Command {
	var ffOnly bool
	return &ucli.Command{
		Name: "pull", Usage: "download and integrate remote changes",
		Flags:  []ucli.Flag{&ucli.BoolFlag{Name: "ff-only", Usage: "refuse local merges", Destination: &ffOnly}},
		Action: noArgsAction(func(ctx context.Context) error { return application.pullOperation(ctx, ffOnly) }),
	}
}

func mergeCommand(application app) *ucli.Command {
	options := mergeOptions{}
	abort := &ucli.BoolFlag{Name: "abort", Usage: "restore the pre-merge workspace", Destination: &options.Abort}
	continueFlag := &ucli.BoolFlag{Name: "continue", Usage: "stage resolved files and commit", Destination: &options.Continue}
	return &ucli.Command{
		Name: "merge", Usage: "continue or abort an in-progress merge",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "message", Aliases: []string{"m"}, Usage: "merge commit message (only with --continue)", Destination: &options.Message},
		},
		MutuallyExclusiveFlags: []ucli.MutuallyExclusiveFlags{{Flags: [][]ucli.Flag{{abort}, {continueFlag}}, Required: true}},
		Action: noArgsAction(func(ctx context.Context) error {
			if options.Abort && strings.TrimSpace(options.Message) != "" {
				return errors.New("--message is valid only with --continue")
			}
			return application.mergeOperation(ctx, options)
		}),
	}
}

func migrateCommand(application app) *ucli.Command {
	options := migrateOptions{}
	return &ucli.Command{
		Name: "migrate", Usage: "migrate a legacy gew workspace to the Git backend",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "to", Usage: "destination backend (git)", Required: true, Destination: &options.Target, Validator: func(value string) error {
				if value != string(WorkspaceGit) {
					return fmt.Errorf("unsupported migration target %q; expected git", value)
				}
				return nil
			}},
			&ucli.BoolFlag{Name: "dry-run", Usage: "validate without writing", Destination: &options.DryRun},
			&ucli.StringFlag{Name: "author-name", Usage: "local Git author name", Destination: &options.AuthorName},
			&ucli.StringFlag{Name: "author-email", Usage: "local Git author email", Destination: &options.AuthorEmail},
		},
		Action: noArgsAction(func(ctx context.Context) error { return application.migrateOperation(ctx, options) }),
	}
}

func pushCommand(application app) *ucli.Command {
	var newBranch string
	return &ucli.Command{
		Name: "push", Usage: "publish queued commits through the forge API",
		Flags:  []ucli.Flag{&ucli.StringFlag{Name: "new-branch", Usage: "commit changes to a new branch", Destination: &newBranch}},
		Action: noArgsAction(func(ctx context.Context) error { return application.pushOperation(ctx, newBranch) }),
	}
}

func releaseCommand(application app) *ucli.Command {
	return &ucli.Command{
		Name: "release", Usage: "publish forge-hosted releases",
		Commands: []*ucli.Command{releaseCreateCommand(application)},
	}
}

func releaseCreateCommand(application app) *ucli.Command {
	options := releaseCreateOptions{}
	return &ucli.Command{
		Name: "create", Usage: "create a tagged release and upload assets", ArgsUsage: "TAG",
		Flags: []ucli.Flag{
			&ucli.StringFlag{Name: "title", Usage: "release title", Required: true, Destination: &options.Title, Validator: nonblank("title")},
			&ucli.StringFlag{Name: "notes-file", Usage: "release notes file (maximum 1 MiB)", Required: true, Destination: &options.NotesFile},
			&ucli.StringSliceFlag{Name: "asset", Usage: "asset file to upload (repeatable)", Required: true, Destination: &options.Assets},
			&ucli.BoolFlag{Name: "draft", Usage: "create a draft release", Destination: &options.Draft},
			&ucli.BoolFlag{Name: "prerelease", Usage: "mark the release as a prerelease", Destination: &options.Prerelease},
			&ucli.BoolFlag{Name: "resume", Usage: "resume an exactly matching existing release", Destination: &options.Resume},
		},
		Action: func(ctx context.Context, command *ucli.Command) error {
			if err := requireArity(command, 1, 1); err != nil {
				return err
			}
			options.Tag = command.Args().First()
			options.Title = strings.TrimSpace(options.Title)
			return application.releaseCreateOperation(ctx, options)
		},
	}
}

func versionCommand(application app) *ucli.Command {
	return &ucli.Command{Name: "version", Usage: "print the version", Action: noArgsAction(func(context.Context) error { return printVersion(application.out) })}
}
