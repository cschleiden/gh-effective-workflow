package main

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

type Workflow struct {
	Name     string
	RefPath  string
	Filename string
	Ref      string
	SHA      string
	YAML     string
}

type Reference struct {
	SourceFilename string
	SourceLine     string
	SourceLineNo   int
}

// Parsing
type WorkflowNode struct {
	Jobs map[string]Job `yaml:"jobs"`
}

type Job struct {
	Uses yaml.Node `yaml:"uses"`
}

func GetReferences(workflows []Workflow, references []ReferencedWorkflow) (map[string][]Reference, error) {
	result := make(map[string][]Reference)

	for _, workflow := range workflows {
		var wfNode WorkflowNode
		if err := yaml.Unmarshal([]byte(workflow.YAML), &wfNode); err != nil {
			return nil, fmt.Errorf("failed to unmarshal yaml: %w", err)
		}

		// Check each job for references to other workflows
		if wfNode.Jobs != nil {
			for _, job := range wfNode.Jobs {
				if !job.Uses.IsZero() {
					if job.Uses.Kind != yaml.ScalarNode || job.Uses.Tag != "!!str" {
						return nil, fmt.Errorf("unexpected node type for uses: %v", job.Uses.Kind)
					}

					workflowRef := job.Uses.Value
					workflowRefLine := job.Uses.Line

					if _, ok := result[workflowRef]; !ok {
						result[workflowRef] = []Reference{}
					}

					result[workflowRef] = append(result[workflowRef], Reference{
						SourceFilename: workflow.Filename,
						SourceLine:     strings.Split(workflow.YAML, "\n")[workflowRefLine-1], // 🙀
						SourceLineNo:   job.Uses.Line,
					})
				}
			}
		}
	}

	return result, nil
}
