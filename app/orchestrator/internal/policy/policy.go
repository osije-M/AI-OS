package policy

import (
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Rule describes a single policy rule.
type Rule struct {
	Name          string   `yaml:"name"`
	Action        string   `yaml:"action"`         // "deny" / "transform" / "allow"
	MatchKeywords []string `yaml:"match_keywords"`
	Reason        string   `yaml:"reason"`
	MaxTaskChars  int      `yaml:"max_task_chars"`
}

type policyFile struct {
	Version string `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// Engine holds the loaded policy rules.
type Engine struct {
	rules []Rule
}

// Decision is the result of evaluating a task against the policy engine.
type Decision struct {
	Action string
	Reason string
	Task   string // transform 后的任务文本（或原文）
}

// Load reads a YAML policy file and returns an Engine.
// On failure it logs the error and returns an empty engine (safe degradation)
// so that the orchestrator can still start up.
func Load(path string) (*Engine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("[policy] failed to read policy file %q: %v — falling back to empty engine", path, err)
		return &Engine{}, nil
	}

	var pf policyFile
	if err := yaml.Unmarshal(data, &pf); err != nil {
		log.Printf("[policy] failed to parse policy file %q: %v — falling back to empty engine", path, err)
		return &Engine{}, nil
	}

	log.Printf("[policy] loaded %d rule(s) from %q (version=%s)", len(pf.Rules), path, pf.Version)
	return &Engine{rules: pf.Rules}, nil
}

// Evaluate runs task through all rules in order and returns a Decision.
//
//   - deny:      any MatchKeyword found (case-insensitive) → short-circuit, return deny.
//   - transform: MaxTaskChars > 0 and len(task) > MaxTaskChars → truncate, update
//                currentTask, record transform decision, and continue evaluating.
//   - allow:     no deny triggered → return allow with (possibly transformed) task.
func (e *Engine) Evaluate(task string) Decision {
	current := task

	var transformDecision *Decision

	for _, rule := range e.rules {
		switch rule.Action {
		case "deny":
			taskLower := strings.ToLower(current)
			for _, kw := range rule.MatchKeywords {
				if strings.Contains(taskLower, strings.ToLower(kw)) {
					return Decision{
						Action: "deny",
						Reason: rule.Reason,
						Task:   current,
					}
				}
			}

		case "transform":
			if rule.MaxTaskChars > 0 && len(current) > rule.MaxTaskChars {
				current = current[:rule.MaxTaskChars]
				d := Decision{
					Action: "transform",
					Reason: fmt.Sprintf("truncated to %d chars", rule.MaxTaskChars),
					Task:   current,
				}
				transformDecision = &d
				// continue evaluating remaining rules with truncated task
			}
		}
	}

	if transformDecision != nil {
		// Update Task to final (possibly further-mutated) value
		transformDecision.Task = current
		return *transformDecision
	}

	return Decision{
		Action: "allow",
		Task:   current,
	}
}
