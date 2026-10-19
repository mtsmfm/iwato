package main

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The default name is a short slug of the profile, valid as a provider
// name with room for a suffix.
func TestProviderNameBase(t *testing.T) {
	for profile, want := range map[string]string{
		"iwato-github":                     "github",
		"iwato-claude":                     "claude",
		"iwato-atlassian":                  "atlassian",
		"Iwato-My_Thing":                   "my-thing",
		"iwato--a..b--":                    "a-b",
		"iwato-9lives":                     "provider-9lives",
		"iwato-":                           "provider",
		"":                                 "provider",
		"iwato-" + strings.Repeat("x", 80): strings.Repeat("x", 40),
	} {
		got := providerNameBase(profile)
		if got != want {
			t.Errorf("%q: %q, want %q", profile, got, want)
		}
		for _, n := range []string{got, providerNameCandidate(got, 2), providerNameCandidate(got, providerNameRetries)} {
			if !providerNamePattern.MatchString(n) {
				t.Errorf("%q: %q is not a valid name", profile, n)
			}
		}
	}
	if got := []string{providerNameCandidate("github", 1), providerNameCandidate("github", 2), providerNameCandidate("github", 3)}; !slices.Equal(got, []string{"github", "github-2", "github-3"}) {
		t.Fatal(got)
	}
	// The page gets them as the placeholders.
	for _, p := range providerProfiles {
		if p.DefaultName != providerNameBase(p.ID) {
			t.Fatalf("%+v", p)
		}
	}
}

func TestIsAlreadyExists(t *testing.T) {
	for _, err := range []error{status.Error(codes.AlreadyExists, "provider already exists"), errors.New("provider x already exists")} {
		if !isAlreadyExists(err) {
			t.Error(err)
		}
	}
	for _, err := range []error{nil, status.Error(codes.InvalidArgument, "bad"), errors.New("timeout")} {
		if isAlreadyExists(err) {
			t.Error(err)
		}
	}
}
