package main

import "testing"

// aliasProviderRules gives a test copy of a built-in profile (another id)
// that profile's initial rules.
func aliasProviderRules(t *testing.T, alias, base string) {
	t.Helper()
	m := providerRulePresets()
	m[alias] = m[base]
	t.Cleanup(func() { delete(m, alias) })
}
