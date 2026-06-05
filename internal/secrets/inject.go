package secrets

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// Resolve looks up every key in keys and returns a map of key→decrypted value.
// Resolution is all-or-nothing: if any key is missing (or fails to decrypt), it
// returns an error naming the offending key and no partial map.
func Resolve(store *SecretStore, keys []string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	for _, key := range keys {
		val, err := store.Get(key)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("secret not found: %s", key)
			}
			return nil, fmt.Errorf("resolving secret %s: %w", key, err)
		}
		out[key] = val
	}
	return out, nil
}

// InjectEnv resolves the secrets named by keys and merges them into a copy of
// existing (os.Environ format: "KEY=value"). A secret value overrides any
// existing entry with the same key. The input slice is not modified.
func InjectEnv(store *SecretStore, keys, existing []string) ([]string, error) {
	resolved, err := Resolve(store, keys)
	if err != nil {
		return nil, err
	}

	// Copy existing, dropping any entries whose key we are about to override.
	out := make([]string, 0, len(existing)+len(resolved))
	for _, kv := range existing {
		name := kv
		if i := strings.IndexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if _, overridden := resolved[name]; !overridden {
			out = append(out, kv)
		}
	}
	for k, v := range resolved {
		out = append(out, k+"="+v)
	}
	return out, nil
}

// ValidateKeys checks that every name in keys is a legal secret name. It returns
// an error listing all invalid names, or nil if all are valid.
func ValidateKeys(keys []string) error {
	var invalid []string
	for _, k := range keys {
		if ValidateName(k) != nil {
			invalid = append(invalid, k)
		}
	}
	if len(invalid) > 0 {
		return fmt.Errorf("invalid secret key name(s): %s", strings.Join(invalid, ", "))
	}
	return nil
}
