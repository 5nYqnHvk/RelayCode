package toolargs

import (
	"encoding/json"
	"fmt"
)

func SanitizeParameters(raw json.RawMessage) (json.RawMessage, map[string]string) {
	if len(raw) == 0 {
		return raw, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw, nil
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return raw, nil
	}
	aliases := map[string]string{}
	for name, value := range props {
		if name != "type" {
			continue
		}
		alias := "_fcc_arg_type"
		if _, exists := props[alias]; exists {
			for i := 2; ; i++ {
				candidate := fmt.Sprintf("_fcc_arg_type_%d", i)
				if _, taken := props[candidate]; !taken {
					alias = candidate
					break
				}
			}
		}
		props[alias] = value
		delete(props, name)
		aliases[alias] = name
		if required, ok := schema["required"].([]any); ok {
			for i, item := range required {
				if item == name {
					required[i] = alias
				}
			}
		}
	}
	if len(aliases) == 0 {
		return raw, nil
	}
	out, err := json.Marshal(schema)
	if err != nil {
		return raw, nil
	}
	return out, aliases
}

func RestoreArgs(index int, toolName, args string, aliases map[string]map[string]string, buffers map[int]string) (string, bool) {
	toolAliases := aliases[toolName]
	if len(toolAliases) == 0 {
		return args, true
	}
	buffered := buffers[index] + args
	var parsed any
	if err := json.Unmarshal([]byte(buffered), &parsed); err != nil {
		buffers[index] = buffered
		return "", false
	}
	delete(buffers, index)
	restored := restoreAliasValue(parsed, toolAliases)
	out, err := json.Marshal(restored)
	if err != nil {
		return args, true
	}
	return string(out), true
}

func RestoreCompleteArgs(toolName, args string, aliases map[string]map[string]string) string {
	restored, ok := RestoreArgs(0, toolName, args, aliases, map[int]string{})
	if !ok || restored == "" {
		return args
	}
	return restored
}

func restoreAliasValue(value any, aliases map[string]string) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range v {
			if original, ok := aliases[key]; ok {
				key = original
			}
			out[key] = restoreAliasValue(item, aliases)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = restoreAliasValue(item, aliases)
		}
		return out
	}
	return value
}
