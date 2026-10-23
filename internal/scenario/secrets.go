package scenario

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// placeholder is how a secret is written where the model sees it.
var placeholder = regexp.MustCompile(`\{secret:([A-Za-z_][A-Za-z0-9_]*)\}`)

// secrets keeps the values of the secrets from the model: what it reads has
// each value replaced with its placeholder, and what it types has each
// placeholder replaced with the value.
type secrets struct {
	values map[string]string
	// names are the names of the secrets with a value, the longest value
	// first, so that a value that holds another is masked whole.
	names []string
}

func newSecrets(values map[string]string) secrets {
	s := secrets{values: values}
	for name, v := range values {
		if v != "" {
			s.names = append(s.names, name)
		}
	}
	sort.Slice(s.names, func(i, j int) bool {
		a, b := values[s.names[i]], values[s.names[j]]
		if len(a) != len(b) {
			return len(a) > len(b)
		}
		return s.names[i] < s.names[j]
	})
	return s
}

// mask replaces the value of each secret in text with its placeholder.
func (s secrets) mask(text string) string {
	for _, name := range s.names {
		text = strings.ReplaceAll(text, s.values[name], "{secret:"+name+"}")
	}
	return text
}

// maskAny masks the strings in v, a value decoded from JSON.
func (s secrets) maskAny(v any) any {
	switch v := v.(type) {
	case string:
		return s.mask(v)
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, e := range v {
			m[k] = s.maskAny(e)
		}
		return m
	case []any:
		l := make([]any, len(v))
		for i, e := range v {
			l[i] = s.maskAny(e)
		}
		return l
	}
	return v
}

// fill replaces each placeholder in text with the value of its secret. A
// placeholder of a secret that was not given is an error, so that it is not
// typed as it is.
func (s secrets) fill(text string) (string, error) {
	var missing []string
	out := placeholder.ReplaceAllStringFunc(text, func(m string) string {
		name := placeholder.FindStringSubmatch(m)[1]
		v, ok := s.values[name]
		if !ok {
			missing = append(missing, name)
			return m
		}
		return v
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("no secret is named %s; the secrets are %s", strings.Join(missing, ", "), s.list())
	}
	return out, nil
}

func (s secrets) list() string {
	if len(s.values) == 0 {
		return "none"
	}
	names := make([]string, 0, len(s.values))
	for name := range s.values {
		names = append(names, "{secret:"+name+"}")
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
