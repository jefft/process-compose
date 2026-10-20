package loader

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/drone/envsubst"
	"github.com/f1bonacc1/process-compose/src/types"
	"gopkg.in/yaml.v3"
)

const hookRuntimeVariableNames = `PC_EVENT|PC_EVENT_TIME|PC_EVENT_JSON|PC_PROJECT_NAME|PC_PROJECT_DIR|` +
	`PC_PROCESS_NAME|PC_PROCESS_NAMESPACE|PC_PROCESS_PID|PC_RESTART_COUNT|PC_TCP_PORTS|PC_UDP_PORTS|` +
	`PC_PORTS_STATUS|PC_PREVIOUS_TCP_PORTS|PC_PREVIOUS_UDP_PORTS|PC_EXIT_CODE|PC_STOP_REASON|PC_EXIT_SIGNAL`

var hookRuntimeReference = regexp.MustCompile(`\$(?:\{[#!]?)?(?:` + hookRuntimeVariableNames + `)\b`)

// expandHookEnvironment preserves runtime-dependent shell expressions only in
// hook commands. Other configuration fields keep the ordinary envsubst rules.
func expandHookEnvironment(input string, expander func(string) string) (string, []string, error) {
	prefix := "__PC_HOOK_RUNTIME_VARIABLE_"
	for {
		if strings.Contains(input, prefix) {
			prefix += "_"
			continue
		}
		collision := false
		checkedExpander := func(name string) string {
			value := expander(name)
			if strings.Contains(value, prefix) {
				collision = true
			}
			return value
		}
		expanded, replacements, err := expandHookInput(input, prefix, checkedExpander, &collision)
		if err != nil {
			return "", nil, err
		}
		// Each inserted token is outside every parameter expression and must
		// survive once. This also catches collisions created by envsubst
		// operators (for example uppercasing a supplied environment value).
		for index := 0; index < len(replacements); index += 2 {
			if strings.Count(expanded, replacements[index]) != 1 {
				collision = true
			}
		}
		if collision {
			prefix += "_"
			continue
		}
		return expanded, replacements, nil
	}
}

// Restore decoded command values, not serialized YAML: a shell expression can
// contain quotes or newlines that must not be injected into YAML syntax.
func restoreHookCommands(project *types.Project, replacements []string, envEscaped string) {
	if len(replacements) == 0 {
		return
	}
	for index := 1; index < len(replacements); index += 2 {
		replacements[index] = strings.ReplaceAll(replacements[index], envEscaped, "$")
	}
	replacer := strings.NewReplacer(replacements...)
	restore := func(hook *types.HookConfig) {
		if hook != nil {
			hook.Command = replacer.Replace(hook.Command)
		}
	}
	restore(project.OnReady)
	for _, process := range project.Processes {
		restore(process.OnStart)
		restore(process.OnReady)
		restore(process.OnExit)
		restore(process.OnPortsChanged)
	}
}

// hookYAMLDocument probes whether the unexpanded input already has a node tree;
// final YAML validation belongs to the normal post-substitution decoder.
func hookYAMLDocument(input string) (*yaml.Node, bool) {
	var document yaml.Node
	valid := yaml.Unmarshal([]byte(input), &document) == nil
	return &document, valid
}

func expandHookInput(input, prefix string, expander func(string) string, collision *bool) (string, []string, error) {
	if !hookRuntimeReference.MatchString(input) {
		expanded, err := envsubst.Eval(input, expander)
		return expanded, nil, err
	}
	document, valid := hookYAMLDocument(input)
	if !valid {
		return expandBeforeHookParsing(input, prefix, expander, collision)
	}
	protected, replacements, err := protectHookCommands(document, input, prefix)
	if err != nil {
		return "", nil, err
	}
	expanded, err := envsubst.Eval(protected, expander)
	return expanded, replacements, err
}

func protectHookCommands(document *yaml.Node, input, prefix string) (string, []string, error) {
	// Aliases can share a hook block or scalar with an ordinary field. Expand
	// them into independent nodes so protecting a hook never protects its source.
	copy, err := copyHookYAMLNode(document, make(map[*yaml.Node]bool))
	if err != nil {
		return "", nil, err
	}
	if len(copy.Content) == 0 {
		return input, nil, nil
	}
	var replacements []string
	for _, command := range hookYAMLCommands(copy) {
		command.Value = protectHookExpressions(command.Value, prefix, &replacements)
	}
	if len(replacements) == 0 {
		return input, nil, nil
	}
	encoded, err := yaml.Marshal(copy)
	return string(encoded), replacements, err
}

func hookYAMLCommands(document *yaml.Node) []*yaml.Node {
	if len(document.Content) == 0 {
		return nil
	}
	root := hookYAMLMapping(document.Content[0])
	var commands []*yaml.Node
	appendCommand := func(hook *yaml.Node) {
		command := hookYAMLMapping(hook)["command"]
		if command != nil && command.Kind == yaml.ScalarNode {
			commands = append(commands, command)
		}
	}
	appendCommand(root["on_ready"])
	for _, process := range hookYAMLMapping(root["processes"]) {
		fields := hookYAMLMapping(process)
		for _, event := range []string{"on_start", "on_ready", "on_exit", "on_ports_changed"} {
			appendCommand(fields[event])
		}
	}
	return commands
}

// Protect an entire balanced parameter expression when any part depends on
// lifecycle metadata. For example, ${OTHER:-$PC_EVENT} must remain whole: loading
// only its inner reference would let envsubst discard or transform the default.
func protectHookExpressions(command, prefix string, replacements *[]string) string {
	var result strings.Builder
	for offset := 0; offset < len(command); {
		start := strings.Index(command[offset:], "${")
		if start < 0 {
			result.WriteString(command[offset:])
			break
		}
		start += offset
		result.WriteString(command[offset:start])
		depth, end := 1, start+2
		for end < len(command) && depth > 0 {
			switch {
			case strings.HasPrefix(command[end:], "${"):
				depth++
				end += 2
			case command[end] == '}':
				depth--
				end++
			default:
				end++
			}
		}
		expression := command[start:end]
		if depth == 0 && hookRuntimeReference.MatchString(expression) {
			token := prefix + strconv.Itoa(len(*replacements)/2) + "__"
			*replacements = append(*replacements, token, expression)
			result.WriteString(token)
		} else {
			result.WriteString(expression)
		}
		offset = end
	}
	return result.String()
}

func copyHookYAMLNode(node *yaml.Node, active map[*yaml.Node]bool) (*yaml.Node, error) {
	if active[node] {
		return nil, fmt.Errorf("cyclic YAML alias in hook configuration")
	}
	active[node] = true
	defer delete(active, node)
	if node.Kind == yaml.AliasNode {
		return copyHookYAMLNode(node.Alias, active)
	}
	copy := *node
	copy.Anchor = ""
	copy.Content = make([]*yaml.Node, len(node.Content))
	for index, child := range node.Content {
		var err error
		copy.Content[index], err = copyHookYAMLNode(child, active)
		if err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

// Resolve aliases and merge keys with explicit fields taking precedence,
// matching the decoder. Callers that mutate the result clone aliases first.
func hookYAMLMapping(node *yaml.Node) map[string]*yaml.Node {
	fields := make(map[string]*yaml.Node)
	for node != nil && node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	if node == nil || node.Kind != yaml.MappingNode {
		return fields
	}
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Tag != "!!merge" {
			continue
		}
		merges := []*yaml.Node{value}
		if value.Kind == yaml.SequenceNode {
			merges = value.Content
		}
		for _, source := range merges {
			for name, field := range hookYAMLMapping(source) {
				if _, exists := fields[name]; !exists {
					fields[name] = field
				}
			}
		}
	}
	for index := 0; index < len(node.Content); index += 2 {
		key, value := node.Content[index], node.Content[index+1]
		if key.Tag != "!!merge" {
			fields[key.Value] = value
		}
	}
	return fields
}
