package loader

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/drone/envsubst"
	"gopkg.in/yaml.v3"
)

// Mask runtime expressions until ordinary substitution makes the source valid
// YAML. Restore both variants into that same source, never isolated scalars:
// indentation, quoting, and flow delimiters all belong to the original context.
func expandBeforeHookParsing(input, prefix string, expander func(string) string, collision *bool) (string, []string, error) {
	var expressions []string
	masked := protectHookExpressions(input, prefix+"PROBE_", &expressions)
	expanded, err := envsubst.Eval(masked, expander)
	if err != nil {
		return "", nil, err
	}
	for index := 0; index < len(expressions); index += 2 {
		if strings.Count(expanded, expressions[index]) != 1 {
			*collision = true
		}
	}
	if *collision || len(expressions) == 0 {
		return expanded, nil, nil
	}
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(expanded), &document); err != nil {
		return "", nil, err
	}
	uses := make(map[*yaml.Node]uint8)
	if err := classifyHookSources(&document, "root", uses, make(map[*yaml.Node]bool)); err != nil {
		return "", nil, err
	}
	hookSource, normalSource, err := restoreHookSources(expanded, prefix, expressions, uses, expander, collision)
	if err != nil {
		return "", nil, err
	}
	if *collision {
		return expanded, nil, nil
	}
	var hooks, normal yaml.Node
	if err := yaml.Unmarshal([]byte(hookSource), &hooks); err != nil {
		return "", nil, err
	}
	if err := yaml.Unmarshal([]byte(normalSource), &normal); err != nil {
		return "", nil, err
	}
	copy, err := copyHookYAMLNode(&normal, make(map[*yaml.Node]bool))
	if err != nil {
		return "", nil, err
	}
	normalCommands := hookYAMLCommandEntries(copy)
	var replacements []string
	for key, command := range hookYAMLCommandEntries(&hooks) {
		for command.Kind == yaml.AliasNode {
			command = command.Alias
		}
		target := normalCommands[key]
		if command.Kind != yaml.ScalarNode || target == nil {
			continue
		}
		token := prefix + strconv.Itoa(len(replacements)/2) + "__"
		replacements = append(replacements, token, command.Value)
		*target = yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: token}
	}
	encoded, err := yaml.Marshal(copy)
	return string(encoded), replacements, err
}

const (
	hookSourceCommand uint8 = 1 << iota
	hookSourceOrdinary
)

// Follow aliases in each usage context. A scalar shared by a hook and an
// ordinary field needs normal expansion at its source, with the hook copied
// independently afterward.
func classifyHookSources(node *yaml.Node, role string, uses map[*yaml.Node]uint8, active map[*yaml.Node]bool) error {
	if active[node] {
		return fmt.Errorf("cyclic YAML alias in hook configuration")
	}
	active[node] = true
	defer delete(active, node)
	switch node.Kind {
	case yaml.AliasNode:
		return classifyHookSources(node.Alias, role, uses, active)
	case yaml.ScalarNode:
		if role == "command" {
			uses[node] |= hookSourceCommand
		} else {
			uses[node] |= hookSourceOrdinary
		}
	case yaml.MappingNode:
		for index := 0; index < len(node.Content); index += 2 {
			key, value := node.Content[index], node.Content[index+1]
			if err := classifyHookSources(key, "", uses, active); err != nil {
				return err
			}
			childRole := hookSourceRole(role, key.Value)
			if key.Tag == "!!merge" {
				childRole = role
			}
			if err := classifyHookSources(value, childRole, uses, active); err != nil {
				return err
			}
		}
	default:
		for _, child := range node.Content {
			if err := classifyHookSources(child, role, uses, active); err != nil {
				return err
			}
		}
	}
	return nil
}

func hookSourceRole(role, key string) string {
	switch {
	case role == "root" && key == "processes":
		return "processes"
	case role == "processes":
		return "process"
	case role == "root" && key == "on_ready":
		return "hook"
	case role == "process" && (key == "on_start" || key == "on_ready" || key == "on_exit" || key == "on_ports_changed"):
		return "hook"
	case role == "hook" && key == "command":
		return "command"
	default:
		return ""
	}
}

func restoreHookSources(source, prefix string, expressions []string, uses map[*yaml.Node]uint8, expander func(string) string, collision *bool) (string, string, error) {
	var hookPairs, normalPairs []string
	for index := 0; index < len(expressions); index += 2 {
		token, expression := expressions[index], expressions[index+1]
		var use uint8
		for node, flags := range uses {
			if strings.Contains(node.Value, token) {
				use |= flags
			}
		}
		if use&hookSourceCommand != 0 {
			hookPairs = append(hookPairs, token, expression)
		}
		if use == hookSourceCommand {
			continue
		}
		value, err := envsubst.Eval(expression, expander)
		if err != nil {
			return "", "", err
		}
		if strings.Contains(value, prefix) {
			*collision = true
		}
		normalPairs = append(normalPairs, token, value)
	}
	return strings.NewReplacer(hookPairs...).Replace(source), strings.NewReplacer(normalPairs...).Replace(source), nil
}

type hookCommandKey struct {
	process string
	event   string
}

func hookYAMLCommandEntries(document *yaml.Node) map[hookCommandKey]*yaml.Node {
	entries := make(map[hookCommandKey]*yaml.Node)
	if len(document.Content) == 0 {
		return entries
	}
	root := hookYAMLMapping(document.Content[0])
	if command := hookYAMLMapping(root["on_ready"])["command"]; command != nil {
		entries[hookCommandKey{event: "on_ready"}] = command
	}
	for name, process := range hookYAMLMapping(root["processes"]) {
		fields := hookYAMLMapping(process)
		for _, event := range []string{"on_start", "on_ready", "on_exit", "on_ports_changed"} {
			if command := hookYAMLMapping(fields[event])["command"]; command != nil {
				entries[hookCommandKey{process: name, event: event}] = command
			}
		}
	}
	return entries
}
