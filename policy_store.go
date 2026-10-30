package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/types"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/memory"
	"go.yaml.in/yaml/v3"
)

// Each sandbox's policy is a git repository (bare, branch main) in the data
// folder's policies/NAME.git, holding one file, policy.yaml: the policy
// Iwato last applied, in OpenShell's policy YAML. One commit per change,
// its message the reason; the last line names the change's kind
// (policyChangeTrailer). A commit is written before the policy is applied
// (its hash goes into the revision's provenance) and the branch moves to it
// only once OpenShell took it, so the branch is never ahead of OpenShell.

const (
	policyFile          = "policy.yaml"
	policyBranch        = plumbing.ReferenceName("refs/heads/main")
	policyChangeTrailer = "Iwato-Change: "
)

// policyStore keeps the repositories; with no folder (the fake, tests)
// they live in memory.
type policyStore struct {
	dir string
	mu  sync.Mutex
	mem map[string]*git.Repository
}

func (p *policyStore) path(name string) string {
	return filepath.Join(p.dir, name+".git")
}

// open opens the sandbox's repository; nil without one unless create.
func (p *policyStore) open(name string, create bool) (*git.Repository, error) {
	if !safeID.MatchString(name) {
		return nil, fmt.Errorf("invalid sandbox name %q", name)
	}
	init := git.InitOptions{DefaultBranch: policyBranch}
	if p.dir == "" {
		p.mu.Lock()
		defer p.mu.Unlock()
		if r := p.mem[name]; r != nil || !create {
			return r, nil
		}
		r, err := git.InitWithOptions(memory.NewStorage(), nil, init)
		if err != nil {
			return nil, err
		}
		if p.mem == nil {
			p.mem = map[string]*git.Repository{}
		}
		p.mem[name] = r
		return r, nil
	}
	r, err := git.PlainOpen(p.path(name))
	if errors.Is(err, git.ErrRepositoryNotExists) {
		if !create {
			return nil, nil
		}
		return git.PlainInitWithOptions(p.path(name), &git.PlainInitOptions{InitOptions: init, Bare: true})
	}
	return r, err
}

// remove deletes the sandbox's repository (the sandbox is deleted).
func (p *policyStore) remove(name string) error {
	if !safeID.MatchString(name) {
		return nil
	}
	if p.dir == "" {
		p.mu.Lock()
		delete(p.mem, name)
		p.mu.Unlock()
		return nil
	}
	return os.RemoveAll(p.path(name))
}

// head is the branch's commit (zero when the repository has none yet).
func (p *policyStore) head(r *git.Repository) (plumbing.Hash, error) {
	ref, err := r.Reference(policyBranch, true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, nil
	}
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

// write stores a commit of content on parent without moving the branch.
func (p *policyStore) write(r *git.Repository, content []byte, parent plumbing.Hash, message string) (plumbing.Hash, error) {
	s := r.Storer
	blob := s.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	blobHash, err := s.SetEncodedObject(blob)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	tree := object.Tree{Entries: []object.TreeEntry{{Name: policyFile, Mode: filemode.Regular, Hash: blobHash}}}
	treeObj := s.NewEncodedObject()
	if err := tree.Encode(treeObj); err != nil {
		return plumbing.ZeroHash, err
	}
	treeHash, err := s.SetEncodedObject(treeObj)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	sig := object.Signature{Name: "Iwato", Email: "iwato@localhost", When: time.Now()}
	c := object.Commit{Author: sig, Committer: sig, Message: message, TreeHash: treeHash}
	if !parent.IsZero() {
		c.ParentHashes = []plumbing.Hash{parent}
	}
	commitObj := s.NewEncodedObject()
	if err := c.Encode(commitObj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.SetEncodedObject(commitObj)
}

// advance moves the branch from old to c, refusing when it moved since.
func (p *policyStore) advance(r *git.Repository, old, c plumbing.Hash) error {
	ref := plumbing.NewHashReference(policyBranch, c)
	if old.IsZero() {
		return r.Storer.SetReference(ref)
	}
	return r.Storer.CheckAndSetReference(ref, plumbing.NewHashReference(policyBranch, old))
}

// content is policy.yaml at commit c.
func (p *policyStore) content(r *git.Repository, c plumbing.Hash) ([]byte, *object.Commit, error) {
	commit, err := r.CommitObject(c)
	if err != nil {
		return nil, nil, err
	}
	f, err := commit.File(policyFile)
	if err != nil {
		return nil, nil, err
	}
	rd, err := f.Reader()
	if err != nil {
		return nil, nil, err
	}
	defer rd.Close()
	b, err := io.ReadAll(rd)
	return b, commit, err
}

// PolicyCommit is one change in a sandbox's policy history.
type PolicyCommit struct {
	SHA     string `json:"sha"`
	Time    int64  `json:"time"`
	Subject string `json:"subject"`
	Message string `json:"message"`
	Kind    string `json:"kind,omitempty"`
	// Root: the first commit, which cannot be reverted.
	Root bool `json:"root,omitempty"`
}

func policyCommitView(c *object.Commit) PolicyCommit {
	msg := strings.TrimRight(c.Message, "\n")
	v := PolicyCommit{SHA: c.Hash.String(), Time: c.Committer.When.Unix(), Root: len(c.ParentHashes) == 0}
	lines := strings.Split(msg, "\n")
	if last := lines[len(lines)-1]; strings.HasPrefix(last, policyChangeTrailer) {
		v.Kind = strings.TrimPrefix(last, policyChangeTrailer)
		lines = lines[:len(lines)-1]
	}
	v.Message = strings.TrimRight(strings.Join(lines, "\n"), "\n")
	v.Subject = lines[0]
	return v
}

// log lists the history from the branch, newest first, up to limit.
func (p *policyStore) log(r *git.Repository, limit int) ([]PolicyCommit, error) {
	out := []PolicyCommit{}
	h, err := p.head(r)
	if err != nil || h.IsZero() {
		return out, err
	}
	for !h.IsZero() && len(out) < limit {
		c, err := r.CommitObject(h)
		if err != nil {
			return out, err
		}
		out = append(out, policyCommitView(c))
		h = plumbing.ZeroHash
		if len(c.ParentHashes) > 0 {
			h = c.ParentHashes[0]
		}
	}
	return out, nil
}

// commitMessage is a change's message: the reason, then its kind.
func commitMessage(kind, message string) string {
	return strings.TrimRight(message, "\n") + "\n\n" + policyChangeTrailer + kind + "\n"
}

// policyStoreDoc is policy.yaml: OpenShell's policy YAML. Endpoints carry
// two fields OpenShell's YAML does not author: credential_binding is
// OpenShell's, advisor_proposed Iwato's, so that a rule approved from a
// proposal keeps OpenShell's marker (no exact-host private-address trust).
type policyStoreDoc struct {
	Version         uint32                  `yaml:"version"`
	Fixed           policyFixedDoc          `yaml:",inline"`
	NetworkPolicies map[string]storeRuleDoc `yaml:"network_policies"`
}

type storeRuleDoc struct {
	Name      string             `yaml:"name"`
	Endpoints []storeEndpointDoc `yaml:"endpoints"`
	Binaries  []binaryDoc        `yaml:"binaries"`
}

type storeEndpointDoc struct {
	endpointDoc       `yaml:",inline"`
	CredentialBinding *storeBindingDoc `yaml:"credential_binding,omitempty"`
	AdvisorProposed   bool             `yaml:"advisor_proposed,omitempty"`
}

type storeBindingDoc struct {
	Provider string `yaml:"provider"`
}

// storeRules renders rules for policy.yaml (the providers' left out).
func storeRules(rules map[string]v1.NetworkPolicyRule) map[string]storeRuleDoc {
	out := map[string]storeRuleDoc{}
	for name, r := range rules {
		if isProviderRule(name) {
			continue
		}
		d := storeRuleDoc{Name: name, Endpoints: []storeEndpointDoc{}, Binaries: []binaryDoc{}}
		for _, e := range r.Endpoints {
			ed := storeEndpointDoc{endpointDoc: policyEndpointDoc(e), AdvisorProposed: e.AdvisorProposed}
			if b := e.CredentialBinding; b != nil {
				ed.CredentialBinding = &storeBindingDoc{Provider: b.Provider}
			}
			d.Endpoints = append(d.Endpoints, ed)
		}
		for _, b := range r.Binaries {
			d.Binaries = append(d.Binaries, binaryDoc{Path: b.Path})
		}
		out[name] = d
	}
	return out
}

// encodeStorePolicy is policy.yaml for a policy.
func encodeStorePolicy(p *v1.SandboxPolicy) ([]byte, error) {
	doc := policyStoreDoc{Version: p.Version, NetworkPolicies: storeRules(p.NetworkPolicies)}
	fixed, err := policyFixedYAML(p)
	if err != nil {
		return nil, err
	}
	if fixed != "" {
		if err := yaml.Unmarshal([]byte(fixed), &doc.Fixed); err != nil {
			return nil, err
		}
	}
	s, err := encodeYAML(doc)
	return []byte(s), err
}

// rulesKey compares rules as policy.yaml writes them.
func rulesKey(rules map[string]v1.NetworkPolicyRule) string {
	s, err := encodeYAML(storeRules(rules))
	if err != nil {
		return "error: " + err.Error()
	}
	return s
}

// decodeStoreRules reads policy.yaml's network_policies.
func decodeStoreRules(b []byte) (map[string]v1.NetworkPolicyRule, error) {
	var doc policyStoreDoc
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", policyFile, err)
	}
	rules := map[string]v1.NetworkPolicyRule{}
	for _, key := range slices.Sorted(maps.Keys(doc.NetworkPolicies)) {
		d := doc.NetworkPolicies[key]
		rule := v1.NetworkPolicyRule{Name: key}
		for _, b := range d.Binaries {
			rule.Binaries = append(rule.Binaries, v1.PolicyNetworkBinary{Path: b.Path})
		}
		for i, e := range d.Endpoints {
			pe, err := e.proto()
			if err != nil {
				return nil, fmt.Errorf("%s: %s endpoints[%d]: %w", policyFile, key, i, err)
			}
			x := policyEndpoint(pe)
			x.AdvisorProposed = e.AdvisorProposed
			if e.CredentialBinding != nil {
				x.CredentialBinding = &types.NetworkCredentialBinding{Provider: e.CredentialBinding.Provider}
			}
			rule.Endpoints = append(rule.Endpoints, x)
		}
		rules[key] = rule
	}
	return rules, nil
}
