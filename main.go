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
	workflow, err := getWorkflowByID(client, baseRepo, strconv.FormatInt(run.WorkflowID, 10))
	if err != nil {
		return fmt.Errorf("failed to get workflow: %w", err)
	}

	if err := viewWorkflowContent(opts, client, baseRepo, workflow, run.HeadBranch); err != nil {
		return fmt.Errorf("failed to view workflow content: %w", err)
	}

	// Show referenced workflows
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

		if err := displayYaml(opts, workflow.ID, workflow.Name, workflow.Base(), refWF.Ref, refWF.SHA, string(workflowContent)); err != nil {
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

	// // Set name to workflow name
	// workflow, err := workflowShared.GetWorkflow(client, repo, result.WorkflowID)
	// if err != nil {
	// 	return nil, err
	// } else {
	// 	result.workflowName = workflow.Name
	// }

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

func viewWorkflowContent(opts *ViewOptions, client api.RESTClient, repo repo.Repository, workflow *wfshared.Workflow, ref string) error {
	yamlBytes, err := getWorkflowContent(client, repo, workflow.Path, ref)
	if err != nil {
		if s, ok := err.(api.HTTPError); ok && s.StatusCode == 404 {
			if ref != "" {
				return fmt.Errorf("could not find workflow file %s on %s, try specifying a different ref", workflow.Base(), ref)
			}
			return fmt.Errorf("could not find workflow file %s, try specifying a branch or tag using `--ref`", workflow.Base())
		}
		return fmt.Errorf("could not get workflow file content: %w", err)
	}

	return displayYaml(opts, workflow.ID, workflow.Name, workflow.Base(), ref, "", string(yamlBytes))
}

func displayYaml(opts *ViewOptions, ID int64, name, fileName, ref, sha, yaml string) error {

	cs := opts.IO.ColorScheme()
	out := opts.IO.Out

	shaStr := ""
	if sha != "" {
		shaStr = fmt.Sprintf(" (%s)", cs.Gray(sha))
	}

	fmt.Fprintf(out, "%s - %s@%s %s\n", cs.Bold(name), cs.Gray(fileName), ref, shaStr)
	fmt.Fprintf(out, "ID: %s", cs.Cyanf("%d", ID))

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
