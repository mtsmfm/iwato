package main

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// A provider's name is optional in Iwato: OpenShell needs one (and cannot
// rename it), so Iwato makes it from the profile (github for iwato-github)
// and, when that is taken, appends -2, -3, …. OpenShell itself only bounds
// the length (253) and would make up a random name; Iwato keeps its own
// pattern (providerNamePattern), which the generated names follow.

// providerNameRetries bounds the suffixes tried.
const providerNameRetries = 100

// providerNameBase is the name a provider of the profile gets by default:
// a short slug of the profile's id without "iwato-", leaving room for a
// suffix.
func providerNameBase(profile string) string {
	id := strings.TrimPrefix(strings.ToLower(profile), "iwato-")
	var b strings.Builder
	dash := false
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		default:
			dash = true
		}
	}
	s := b.String()
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = strings.TrimSuffix("provider-"+s, "-")
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// providerNameCandidate is the i-th name tried (from 1): base, base-2, ….
func providerNameCandidate(base string, i int) string {
	if i <= 1 {
		return base
	}
	return base + "-" + strconv.Itoa(i)
}

// isAlreadyExists tells an error of Create for a name in use.
func isAlreadyExists(err error) bool {
	return err != nil && (v1.IsAlreadyExists(err) || status.Code(err) == codes.AlreadyExists || strings.Contains(strings.ToLower(err.Error()), "already exists"))
}

// createProviderNamed creates the provider under name, or, when name is
// empty, under the first free one of the profile's (base, base-2, …): it
// skips the names listed and, as another client may create one meanwhile,
// tries the next when the gateway answers that it exists. It returns the
// name created.
func createProviderNamed(ctx context.Context, rt providerRuntime, name, profile string, creds map[string]string) (string, error) {
	if name != "" {
		return name, rt.CreateProvider(ctx, name, profile, creds)
	}
	var taken []string
	if list, _, err := rt.ListProviders(ctx); err == nil {
		for _, p := range list {
			taken = append(taken, p.Name)
		}
	}
	base := providerNameBase(profile)
	for i := 1; i <= providerNameRetries; i++ {
		n := providerNameCandidate(base, i)
		if slices.Contains(taken, n) {
			continue
		}
		err := rt.CreateProvider(ctx, n, profile, creds)
		if !isAlreadyExists(err) {
			return n, err
		}
	}
	return "", errCode("provider_names_taken", map[string]any{"first": base, "last": providerNameCandidate(base, providerNameRetries)}, fmt.Sprintf("%s から %s まで使われています。名前を入力してください", base, providerNameCandidate(base, providerNameRetries)))
}

func init() {
	for i := range providerProfiles {
		providerProfiles[i].DefaultName = providerNameBase(providerProfiles[i].ID)
	}
}
