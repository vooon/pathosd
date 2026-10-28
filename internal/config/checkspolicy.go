package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// Checks policies: how several checks combine into the VIP health. Any other
// value is a boolean expression over the check names (see CompileChecksPolicy).
const (
	ChecksPolicyAll = "all" // every check healthy (AND)
	ChecksPolicyAny = "any" // at least one check healthy (OR)
)

// checkNameRe is the shape a check name must have so it can be referenced
// from a checks_policy expression.
var checkNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateCheckName reports why name cannot be used as a check name.
func ValidateCheckName(name string) error {
	if !checkNameRe.MatchString(name) {
		return fmt.Errorf("must match %s", checkNameRe)
	}
	if name == ChecksPolicyAll || name == ChecksPolicyAny {
		return fmt.Errorf("%q is reserved", name)
	}
	return nil
}

// ChecksEvaluator folds per-check health into one VIP health.
type ChecksEvaluator struct {
	policy  string
	names   []string
	program *vm.Program // nil for all/any
}

// CompileChecksPolicy builds the evaluator for a checks_policy value given
// the VIP's check names. "all" and "any" are shortcuts; anything else is an
// expr-lang boolean expression (https://expr-lang.org) in which every check
// name is a boolean variable, e.g. "web || (dns && ping)". Unknown names and
// non-boolean results are rejected at compile time.
func CompileChecksPolicy(policy string, names []string) (*ChecksEvaluator, error) {
	if policy == "" {
		policy = ChecksPolicyAll
	}
	ev := &ChecksEvaluator{policy: policy, names: append([]string(nil), names...)}
	switch policy {
	case ChecksPolicyAll, ChecksPolicyAny:
		return ev, nil
	}
	env := make(map[string]bool, len(names))
	for _, n := range names {
		env[n] = true
	}
	prog, err := expr.Compile(policy, expr.Env(env), expr.AsBool())
	if err != nil {
		known := append([]string(nil), names...)
		sort.Strings(known)
		return nil, fmt.Errorf("%w (known checks: %s)", err, strings.Join(known, ", "))
	}
	ev.program = prog
	return ev, nil
}

// Policy returns the policy string the evaluator was compiled from.
func (e *ChecksEvaluator) Policy() string { return e.policy }

// Eval returns the combined health for the given per-check health. Checks
// missing from healthy count as unhealthy.
func (e *ChecksEvaluator) Eval(healthy map[string]bool) bool {
	if e.program == nil {
		all, anyOK := true, false
		for _, n := range e.names {
			if healthy[n] {
				anyOK = true
			} else {
				all = false
			}
		}
		if e.policy == ChecksPolicyAny {
			return anyOK
		}
		return all && len(e.names) > 0
	}
	env := make(map[string]bool, len(e.names))
	for _, n := range e.names {
		env[n] = healthy[n]
	}
	out, err := expr.Run(e.program, env)
	if err != nil {
		return false // cannot happen for a compiled bool expression over bools
	}
	ok, _ := out.(bool)
	return ok
}
