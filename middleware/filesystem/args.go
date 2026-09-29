package filesystem

import (
	"fmt"
	"math"
)

// args reads model-supplied tool arguments decoded from JSON.
type args map[string]any

func (a args) requiredString(key string) (string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return "", fmt.Errorf("missing required argument %q", key)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string, got %T", key, v)
	}
	return s, nil
}

func (a args) optionalString(key, def string) (string, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return def, nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("argument %q must be a string, got %T", key, v)
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

func (a args) optionalInt(key string, def int) (int, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return def, nil
	}
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("argument %q must be an integer, got %v", key, n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("argument %q must be an integer, got %T", key, v)
	}
}

func (a args) optionalBool(key string, def bool) (bool, error) {
	v, ok := a[key]
	if !ok || v == nil {
		return def, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("argument %q must be a boolean, got %T", key, v)
	}
	return b, nil
}
