package main

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MakeNowJust/heredoc"
	"github.com/cli/cli/v2/pkg/cmd/factory"
	"github.com/cli/cli/v2/pkg/cmd/run/shared"
	wfshared "github.com/cli/cli/v2/pkg/cmd/workflow/shared"
	"github.com/cli/cli/v2/pkg/cmdutil"
	"github.com/cli/cli/v2/pkg/iostreams"
	"github.com/cli/go-gh"
	"github.com/cli/go-gh/pkg/api"
	"github.com/cli/go-gh/pkg/markdown"
	repo "github.com/cli/go-gh/pkg/repository"
	"github.com/cli/go-gh/pkg/tableprinter"
	"github.com/cli/go-gh/pkg/term"
	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "gh-effective-workflow",
	Short: "Display effective workflow",
}

func main() {
	rootCmd.AddCommand(NewCmdView())
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

type ViewOptions struct {
	RunID string

	IO  *iostreams.IOStreams
	Now func() time.Time
}

func NewCmdView() *cobra.Command {
	f := factory.New("1.0.0") // TODO: version

	opts := &ViewOptions{
		IO:  f.IOStreams,
		Now: time.Now,
	}

	cmd := &cobra.Command{
		Use:   "view [<run-id>]",
		Short: "View the effective workflow file for a workflow run",
		Args:  cobra.MaximumNArgs(1),
		Example: heredoc.Doc(`
			# Interactively select a run to view, optionally selecting a single job
			$ gh effective-workflow view

			# View a specific run
			$ gh effective-workflow view 12345
		`),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmdutil.FlagErrorf("run or job ID required when not running interactively")
			} else if len(args) > 0 {
				opts.RunID = args[0]
			}

			return runView(opts)
		},
	}

	return cmd
}

func runView(opts *ViewOptions) error {
	baseRepo, err := gh.CurrentRepository()
	if err != nil {
		return fmt.Errorf("failed to determine base repo: %w", err)
	}

	client, err := gh.RESTClient(nil)
	if err != nil {
		return fmt.Errorf("failed to create http client: %w", err)
	}

	runID := opts.RunID

	run, err := getRun(client, baseRepo, runID)
	if err != nil {
		return fmt.Errorf("failed to get run: %w", err)
	}

	// Get & show workflow
	fmt.Fprintln(opts.IO.Out, opts.IO.ColorScheme().CyanBold("Workflow file for this run\n"))

	workflow, err := getWorkflowByID(client, baseRepo, strconv.FormatInt(run.WorkflowID, 10))
	if err != nil {
		return fmt.Errorf("failed to get workflow: %w", err)
	}

	callingWorkflowContent, err := getWorkflowContent(client, baseRepo, workflow.Path, run.HeadBranch)
	if err != nil {
		return fmt.Errorf("failed to get workflow content: %w", err)
	}

	if err := displayYaml(opts, workflow.Name, workflow.Base(), run.HeadBranch, run.HeadSha, string(callingWorkflowContent), nil); err != nil {
		return fmt.Errorf("failed to display yaml: %w", err)
	}

	// Calculate referenced workflows
	wfs := make([]Workflow, 0)

	callingWorkflow := Workflow{
		Name:     workflow.Name,
		RefPath:  workflow.Path,
		Filename: workflow.Base(),
		Ref:      run.HeadBranch,
		SHA:      run.HeadSha,
		YAML:     string(callingWorkflowContent),
	}

	for _, refWF := range run.ReferencedWorkflows {
		// Parse referenced workflow in the form "octocat/Hello-World/.github/workflows/deploy.yml@main",
		t := strings.Split(refWF.Path, "@")
		path := t[0]
		ref := t[1]

		// Parse path
		t = strings.Split(path, "/")
		nwo := strings.Join(t[0:2], "/")
		path = strings.Join(t[2:], "/")

		referencedRepo, err := repo.Parse(nwo)
		if err != nil {
			return fmt.Errorf("failed to parse referenced repo: %w", err)
		}

		workflow, err := getWorkflowByID(client, referencedRepo, path)
		if err != nil {
			return fmt.Errorf("failed to get workflow: %w", err)
		}

		workflowContent, err := getWorkflowContent(client, referencedRepo, path, ref)
		if err != nil {
			return fmt.Errorf("failed to get workflow content: %w", err)
		}

		wf := Workflow{
			Name:     workflow.Name,
			RefPath:  refWF.Path,
			Filename: workflow.Base(),
			Ref:      ref,
			SHA:      refWF.SHA,
			YAML:     string(workflowContent),
		}

		wfs = append(wfs, wf)
	}

	// Calculate refs
	allWorkflows := append([]Workflow{callingWorkflow}, wfs...)
	refs, err := GetReferences(allWorkflows, run.ReferencedWorkflows)
	if err != nil {
		return fmt.Errorf("failed to get references: %w", err)
	}

	// Output
	for _, wf := range wfs {
		fmt.Fprintln(opts.IO.Out, opts.IO.ColorScheme().CyanBold("Called reusable workflow file\n"))

		if err := displayYaml(opts, wf.Name, wf.Filename, wf.Ref, wf.SHA, wf.YAML, refs[wf.RefPath]); err != nil {
			return fmt.Errorf("failed to display yaml: %w", err)
		}
	}

	return nil
}

type ReferencedWorkflow struct {
	Path string `json:"path,omitempty"`
	SHA  string `json:"sha,omitempty"`
	Ref  string `json:"ref,omitempty"`
}

type Run struct {
	shared.Run

	ReferencedWorkflows []ReferencedWorkflow `json:"referenced_workflows"`
}

func getRun(client api.RESTClient, repo repo.Repository, runID string) (*Run, error) {
	var result Run

	path := fmt.Sprintf("repos/%s/%s/actions/runs/%s?exclude_pull_requests=true", repo.Owner(), repo.Name(), runID)

	err := client.Get(path, &result)
	if err != nil {
		return nil, err
	}

	return &result, nil
}

func getWorkflowByID(client api.RESTClient, repo repo.Repository, ID string) (*wfshared.Workflow, error) {
	var workflow wfshared.Workflow

	path := fmt.Sprintf("repos/%s/%s/actions/workflows/%s", repo.Owner(), repo.Name(), url.PathEscape(ID))
	if err := client.Get(path, &workflow); err != nil {
		return nil, err
	}

	return &workflow, nil
}

func getWorkflowContent(client api.RESTClient, repo repo.Repository, wfPath string, ref string) ([]byte, error) {
	path := fmt.Sprintf("repos/%s/%s/contents/%s", repo.Owner(), repo.Name(), wfPath)
	if ref != "" {
		q := fmt.Sprintf("?ref=%s", url.QueryEscape(ref))
		path = path + q
	}

	type Result struct {
		Content string
	}

	var result Result
	err := client.Get(path, &result)
	if err != nil {
		return nil, err
	}

	decoded, err := base64.StdEncoding.DecodeString(result.Content)
	if err != nil {
		return nil, fmt.Errorf("failed to decode workflow file: %w", err)
	}

	return decoded, nil
}

func displayYaml(opts *ViewOptions, name, fileName, ref, sha, yaml string, refs []Reference) error {
	cs := opts.IO.ColorScheme()
	out := opts.IO.Out

	shaStr := ""
	if sha != "" {
		shaStr = fmt.Sprintf(" (%s)", cs.Gray(sha))
	}

	// Heading
	fmt.Fprintf(out, "%s - %s@%s%s\n", name, cs.Gray(fileName), ref, shaStr)

	if len(refs) > 0 {
		// References
		fmt.Fprintln(out)

		// Heading
		heading := "reference"
		if len(refs) > 1 {
			heading = "references"
		}
		fmt.Fprintln(out, cs.Gray(fmt.Sprintf("%d %s", len(refs), heading)))

		terminal := term.FromEnv()
		termWidth, _, _ := terminal.Size()
		t := tableprinter.New(terminal.Out(), terminal.IsTerminalOutput(), termWidth)

		for _, ref := range refs {
			codeBlock := fmt.Sprintf("```yaml\n%s\n```", strings.TrimSpace(ref.SourceLine))
			rendered, err := markdown.Render(codeBlock,
				markdown.WithTheme(opts.IO.TerminalTheme()),
				markdown.WithoutIndentation(),
				markdown.WithWrap(0))
			if err != nil {
				return err
			}

			line := strings.Split(rendered, "\n")[2] // Ignore code fencing
			line = strings.ReplaceAll(line, "\n", "")

			// fmt.Fprintf(out, "- %s\t%s:\t%s\n", cs.Gray(ref.SourceFilename), cs.Gray(strconv.Itoa(ref.SourceLineNo)), line)
			t.AddField(cs.Gray(ref.SourceFilename))
			t.AddField(cs.Gray(fmt.Sprintf("%4d", ref.SourceLineNo)))
			t.AddField(line)
			t.EndRow()
		}

		if err := t.Render(); err != nil {
			return err
		}
	}

	// YAML content
	codeBlock := fmt.Sprintf("```yaml\n%s\n```", yaml)
	rendered, err := markdown.Render(codeBlock,
		markdown.WithTheme(opts.IO.TerminalTheme()),
		markdown.WithoutIndentation(),
		markdown.WithWrap(0))
	if err != nil {
		return err
	}

	_, err = fmt.Fprint(opts.IO.Out, rendered)
	return err
}
