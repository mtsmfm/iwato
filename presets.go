package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"go.yaml.in/yaml/v3"
)

// A network preset is a set of rules for a sandbox's policy, as a YAML
// file: Iwato's (presets/*.yaml, embedded) and the owner's
// (<the folder of iwato.json>/presets/*.yaml, read at start and whenever
// the list is shown). The rules are OpenShell's policy YAML's
// network_policies, so a rule can be copied from or into a policy:
//
//	id: my-tools             # a-z, 0-9, -; the owner's are "user:ID"
//	label: 表示名
//	note: 説明
//	default: true            # optional: checked in the create form
//	requires: [tools]        # optional: preset IDs that come with this one
//	params:                  # optional
//	  - name: repo           # used as ${repo}
//	    label: リポジトリ
//	    placeholder: OWNER/REPO
//	    pattern: '[A-Za-z0-9-]+/[A-Za-z0-9._-]+'   # the whole value
//	network_policies:
//	  allow_example_com_443:
//	    endpoints:
//	      - { host: example.com, port: 443, protocol: rest, enforcement: enforce, access: read-only }
//	    binaries: [/usr/bin/curl]
//
// A parameter's value must match its pattern and Iwato's own set (letters,
// digits, . _ - /; no "..", no glob characters), and goes only into rule
// names (with characters other than letters, digits and _ as _), hosts and
// paths (the endpoint's and its rules'), where ${NAME} stands.
//
// A preset goes into the first policy when the sandbox is created with it,
// or into the policy later (the policy tab), through the same merge an
// edit makes (Sandbox.Presets lists both kinds; a recreate creates the new
// sandbox with them and then puts the old policy back). Taking one out
// removes its rules only where they are still as the presets made them.
//
// Iwato's presets keep every endpoint inspected (rest, enforce), as the
// providers' are, so that attaching a provider never makes an endpoint
// ambiguous, and name programs (OpenShell allows no program for a rule
// without binaries; "/**" is any program).

//go:embed presets/*.yaml
var builtinPresetFiles embed.FS

// userPresetPrefix starts the IDs of the owner's presets, so that they
// never shadow Iwato's.
const userPresetPrefix = "user:"

type presetDoc struct {
	ID              string                   `yaml:"id"`
	Label           string                   `yaml:"label"`
	Note            string                   `yaml:"note,omitempty"`
	Default         bool                     `yaml:"default,omitempty"`
	Requires        []string                 `yaml:"requires,omitempty"`
	Params          []presetParamDoc         `yaml:"params,omitempty"`
	NetworkPolicies map[string]presetRuleDoc `yaml:"network_policies"`
}

type presetParamDoc struct {
	Name        string `yaml:"name" json:"name"`
	Label       string `yaml:"label" json:"label"`
	Placeholder string `yaml:"placeholder,omitempty" json:"placeholder,omitempty"`
	Pattern     string `yaml:"pattern" json:"-"`
}

type presetRuleDoc struct {
	Name      string        `yaml:"name,omitempty"`
	Endpoints []endpointDoc `yaml:"endpoints"`
	Binaries  []binaryDoc   `yaml:"binaries"`
}

// networkPreset is one preset, as the lists show it.
type networkPreset struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note"`
	// Default: checked in the create form. Requires: presets that come
	// with this one.
	Default  bool             `json:"default,omitempty"`
	Requires []string         `json:"requires,omitempty"`
	Params   []presetParamDoc `json:"params"`
	// Source is builtin or user; File the owner's file name.
	Source string   `json:"source"`
	File   string   `json:"file,omitempty"`
	Hosts  []string `json:"hosts"`
	// Allows is what the rules let through, for the owner to read.
	Allows []presetAllow `json:"allows"`
	// rules hold ${NAME} where the parameters go.
	rules    map[string]v1.NetworkPolicyRule
	patterns map[string]*regexp.Regexp
}

// presetFileError is a preset file that could not be read: Error is the
// Japanese text, Code and Params what the page translates (errCode's).
type presetFileError struct {
	File   string         `json:"file"`
	Error  string         `json:"error"`
	Code   string         `json:"code,omitempty"`
	Params map[string]any `json:"params,omitempty"`
	en     string         // the log's text (english)
}

func newPresetFileError(file string, err error) presetFileError {
	e := presetFileError{File: file, Error: err.Error(), en: english(err)}
	var c *codedError
	if errors.As(err, &c) && c.msg == e.Error {
		e.Code, e.Params = c.code, c.params
	}
	return e
}

// PresetRef is a preset a sandbox has, with its parameters. In JSON it is
// the ID alone when there are none.
type PresetRef struct {
	ID     string            `json:"id"`
	Params map[string]string `json:"params,omitempty"`
}

func (r *PresetRef) UnmarshalJSON(b []byte) error {
	var id string
	if json.Unmarshal(b, &id) == nil {
		*r = PresetRef{ID: id}
		return nil
	}
	type plain PresetRef
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*r = PresetRef(p)
	return nil
}

func (r PresetRef) MarshalJSON() ([]byte, error) {
	if len(r.Params) == 0 {
		return json.Marshal(r.ID)
	}
	type plain PresetRef
	return json.Marshal(plain(r))
}

func (r PresetRef) same(o PresetRef) bool {
	return r.ID == o.ID && maps.Equal(r.Params, o.Params)
}

// label names it for the owner: the preset's label and the parameters.
func (r PresetRef) label() string {
	label := r.ID
	if p := findPreset(r.ID); p != nil {
		label = p.Label
	}
	var values []string
	for _, k := range slices.Sorted(maps.Keys(r.Params)) {
		values = append(values, r.Params[k])
	}
	if len(values) > 0 {
		label += " (" + strings.Join(values, ", ") + ")"
	}
	return label
}

var (
	presetIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	presetParamName    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,19}$`)
	presetParamValue   = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	presetPlaceholder  = regexp.MustCompile(`\$\{([^}]*)\}`)
	presetRuleNameChar = regexp.MustCompile(`[^A-Za-z0-9_]`)
)

// presetCatalog is Iwato's presets and the owner's (read again by
// reloadUserPresets).
var presetCatalog = struct {
	sync.RWMutex
	user   []networkPreset
	errors []presetFileError
	dir    string
}{}

// builtinPresets are Iwato's, read on first use (after the provider
// profiles they are checked against: providers_profiles.go's init).
var builtinPresets = sync.OnceValue(func() []networkPreset {
	list, errs := readPresetDir(builtinPresetFiles, "presets", "builtin")
	if len(errs) > 0 {
		panic(fmt.Sprintf("Iwato's presets: %+v", errs))
	}
	return list
})

// setUserPresetDir sets the owner's folder and reads it.
func setUserPresetDir(dir string) {
	presetCatalog.Lock()
	presetCatalog.dir = dir
	presetCatalog.Unlock()
	reloadUserPresets()
}

// reloadUserPresets reads the owner's folder again. A file that cannot be
// used is logged and listed, the others are kept.
func reloadUserPresets() {
	presetCatalog.RLock()
	dir := presetCatalog.dir
	presetCatalog.RUnlock()
	var list []networkPreset
	var errs []presetFileError
	if dir != "" {
		if _, err := os.Stat(dir); err == nil {
			list, errs = readPresetDir(os.DirFS(dir), ".", "user")
		} else if !errors.Is(err, fs.ErrNotExist) {
			errs = []presetFileError{newPresetFileError(dir, err)}
		}
	}
	presetCatalog.Lock()
	defer presetCatalog.Unlock()
	if !slices.EqualFunc(errs, presetCatalog.errors, func(a, b presetFileError) bool { return a.File == b.File && a.Error == b.Error }) {
		for _, e := range errs {
			log.Printf("preset %s: %s", e.File, e.en)
		}
	}
	presetCatalog.user, presetCatalog.errors = list, errs
}

// readPresetDir reads the *.yaml files of dir, in name order.
func readPresetDir(fsys fs.FS, dir, source string) ([]networkPreset, []presetFileError) {
	names, err := fs.Glob(fsys, filepath.ToSlash(filepath.Join(dir, "*.yaml")))
	if err != nil {
		return nil, []presetFileError{newPresetFileError(dir, err)}
	}
	sort.Strings(names)
	var list []networkPreset
	var errs []presetFileError
	seen := map[string]string{}
	for _, name := range names {
		file := filepath.Base(name)
		b, err := fs.ReadFile(fsys, name)
		if err == nil && len(b) > 256<<10 {
			err = errCodeEn("preset_too_large", nil, "256 KiB を超えています", "Larger than 256 KiB", nil)
		}
		var p *networkPreset
		if err == nil {
			p, err = parsePresetYAML(b, source)
		}
		if err == nil && seen[p.ID] != "" {
			id := strings.TrimPrefix(p.ID, userPresetPrefix)
			err = errCodeEn("preset_id_taken", map[string]any{"id": id, "file": seen[p.ID]},
				fmt.Sprintf("ID %s は %s が使っています", id, seen[p.ID]), fmt.Sprintf("ID %s is used by %s", id, seen[p.ID]), nil)
		}
		if err != nil {
			errs = append(errs, newPresetFileError(file, err))
			continue
		}
		seen[p.ID] = file
		if source == "user" {
			p.File = file
		}
		list = append(list, *p)
	}
	return list, errs
}

// parsePresetYAML reads one preset file. Unknown fields are errors.
func parsePresetYAML(b []byte, source string) (*networkPreset, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var doc presetDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errCodeEn("preset_documents", nil, "1 つのファイルに YAML 文書は 1 つです", "One YAML document per file", nil)
	}
	if !presetIDPattern.MatchString(doc.ID) {
		return nil, errCodeEn("preset_id_invalid", map[string]any{"id": doc.ID},
			fmt.Sprintf("id %q は英小文字・数字・- の 40 文字以内にしてください", doc.ID), fmt.Sprintf("id %q: up to 40 lowercase letters, digits and -", doc.ID), nil)
	}
	if strings.TrimSpace(doc.Label) == "" || len(doc.Label) > 200 || len(doc.Note) > 2000 {
		return nil, errCodeEn("preset_label_invalid", nil, "label (200 バイトまで) が要ります。note は 2000 バイトまでです", "label is required (up to 200 bytes); note is up to 2000 bytes", nil)
	}
	p := &networkPreset{ID: doc.ID, Label: doc.Label, Note: doc.Note, Default: doc.Default, Requires: doc.Requires, Source: source, Params: []presetParamDoc{}, Hosts: []string{},
		rules: map[string]v1.NetworkPolicyRule{}, patterns: map[string]*regexp.Regexp{}}
	if source == "user" {
		p.ID = userPresetPrefix + doc.ID
	}
	if len(doc.Params) > 4 {
		return nil, errCodeEn("preset_params_too_many", nil, "params は 4 個までです", "Up to 4 params", nil)
	}
	for _, d := range doc.Params {
		if !presetParamName.MatchString(d.Name) || p.patterns[d.Name] != nil {
			return nil, errCodeEn("preset_param_name_invalid", map[string]any{"name": d.Name},
				fmt.Sprintf("params の name %q は英小文字で始まる英小文字・数字・_ にし、重ねないでください", d.Name),
				fmt.Sprintf("params name %q: lowercase letters, digits and _, starting with a letter, each once", d.Name), nil)
		}
		if strings.TrimSpace(d.Label) == "" || d.Pattern == "" {
			return nil, errCodeEn("preset_param_incomplete", map[string]any{"name": d.Name},
				fmt.Sprintf("params の %s に label と pattern が要ります", d.Name), fmt.Sprintf("params %s needs a label and a pattern", d.Name), nil)
		}
		re, err := regexp.Compile(`^(?:` + d.Pattern + `)$`)
		if err != nil {
			return nil, errCodeEn("preset_param_pattern_invalid", map[string]any{"name": d.Name, "detail": err.Error()},
				fmt.Sprintf("params の %s の pattern: %v", d.Name, err), fmt.Sprintf("params %s pattern: %v", d.Name, err), err)
		}
		p.patterns[d.Name] = re
		p.Params = append(p.Params, d)
	}
	if len(doc.NetworkPolicies) == 0 {
		return nil, errCodeEn("preset_rules_empty", nil, "network_policies に規則がありません", "network_policies has no rules", nil)
	}
	sample := map[string]string{}
	for name := range p.patterns {
		sample[name] = "x"
	}
	for key, d := range doc.NetworkPolicies {
		if d.Name != "" && d.Name != key {
			return nil, errCodeEn("preset_rule_name_mismatch", map[string]any{"rule": key},
				fmt.Sprintf("規則 %s: name は書かないか、キーと同じにしてください", key), fmt.Sprintf("rule %s: leave out name or make it the key", key), nil)
		}
		rule := v1.NetworkPolicyRule{Name: key}
		if len(d.Endpoints) == 0 || len(d.Binaries) == 0 {
			return nil, errCodeEn("preset_rule_incomplete", map[string]any{"rule": key},
				fmt.Sprintf("規則 %s: endpoints と binaries が要ります (binaries がないとどのプログラムも通れません。すべてなら \"/**\")", key),
				fmt.Sprintf("rule %s: endpoints and binaries are required (without binaries no program is allowed; \"/**\" for all)", key), nil)
		}
		for _, b := range d.Binaries {
			if !strings.HasPrefix(b.Path, "/") || !printable(b.Path, 512) {
				return nil, errCodeEn("preset_binary_invalid", map[string]any{"rule": key, "binary": b.Path},
					fmt.Sprintf("規則 %s: プログラム %q は / で始まる絶対パスにしてください", key, b.Path), fmt.Sprintf("rule %s: program %q must be an absolute path starting with /", key, b.Path), nil)
			}
			rule.Binaries = append(rule.Binaries, v1.PolicyNetworkBinary{Path: b.Path})
		}
		for i, e := range d.Endpoints {
			pe, err := e.proto()
			if err != nil {
				return nil, errCodeEn("preset_endpoint_invalid", map[string]any{"rule": key, "index": i, "detail": err.Error()},
					fmt.Sprintf("規則 %s endpoints[%d]: %v", key, i, err), fmt.Sprintf("rule %s endpoints[%d]: %v", key, i, err), err)
			}
			x := profileEndpoint(pe)
			if len(x.Ports) == 0 && x.Port != 0 {
				x.Ports = []uint32{x.Port}
			}
			if len(x.Ports) > 0 {
				x.Port = x.Ports[0]
			}
			if x.Port == 0 {
				return nil, errCodeEn("preset_endpoint_port_missing", map[string]any{"rule": key, "index": i},
					fmt.Sprintf("規則 %s endpoints[%d]: port がありません", key, i), fmt.Sprintf("rule %s endpoints[%d]: no port", key, i), nil)
			}
			rule.Endpoints = append(rule.Endpoints, x)
		}
		// Every ${NAME} is a parameter; the result is a good rule.
		var bad error
		walkPresetStrings(&rule, func(s string) string {
			for _, m := range presetPlaceholder.FindAllStringSubmatch(s, -1) {
				if _, ok := p.patterns[m[1]]; !ok && bad == nil {
					bad = presetParamUnknown(key, m[1])
				}
			}
			return s
		})
		for _, m := range presetPlaceholder.FindAllStringSubmatch(key, -1) {
			if _, ok := p.patterns[m[1]]; !ok && bad == nil {
				bad = presetParamUnknown(key, m[1])
			}
		}
		if bad != nil {
			return nil, bad
		}
		for _, b := range rule.Binaries {
			if strings.Contains(b.Path, "${") {
				return nil, errCodeEn("preset_param_in_binary", map[string]any{"rule": key},
					fmt.Sprintf("規則 %s: params はプログラムに使えません", key), fmt.Sprintf("rule %s: params cannot go into programs", key), nil)
			}
		}
		p.rules[key] = rule
	}
	sampled, err := p.build(sample)
	if err != nil {
		return nil, err
	}
	if source == "user" {
		if err := presetFitsProviders(sampled); err != nil {
			return nil, err
		}
	}
	for _, key := range slices.Sorted(maps.Keys(p.rules)) {
		for _, e := range p.rules[key].Endpoints {
			if !slices.Contains(p.Hosts, e.Host) {
				p.Hosts = append(p.Hosts, e.Host)
			}
		}
	}
	p.Allows = presetAllows(p.rules)
	return p, nil
}

// presetAllow is one destination of a preset: "host:port" (and the
// endpoint's path), with the methods and paths its rules allow.
type presetAllow struct {
	Target string            `json:"target"`
	Rules  []presetAllowRule `json:"rules"`
}

// presetAllowRule is the paths allowed the same methods.
type presetAllowRule struct {
	Methods []string `json:"methods"`
	Paths   []string `json:"paths"`
}

// presetAllows lists the rules by destination, in host order.
func presetAllows(rules map[string]v1.NetworkPolicyRule) []presetAllow {
	byTarget := map[string]map[string][]string{} // target, path, methods
	for _, r := range rules {
		for _, e := range r.Endpoints {
			target := e.Host + ":" + joinPorts(effectivePorts(e)) + e.Path
			paths := byTarget[target]
			if paths == nil {
				paths = map[string][]string{}
				byTarget[target] = paths
			}
			add := func(path string, methods ...string) {
				for _, m := range methods {
					if !slices.Contains(paths[path], m) {
						paths[path] = append(paths[path], m)
					}
				}
			}
			for _, x := range e.Rules {
				if x.Allow != nil {
					add(x.Allow.Path, x.Allow.Method)
				}
			}
			if len(e.Rules) == 0 {
				methods := accessMethods[accessNames[e.Access]]
				if methods == nil { // not inspected, or no preset: everything
					methods = []string{"*"}
				}
				add("**", methods...)
			}
		}
	}
	out := []presetAllow{}
	for _, target := range slices.Sorted(maps.Keys(byTarget)) {
		a := presetAllow{Target: target}
		paths := byTarget[target]
		for _, path := range slices.Sorted(maps.Keys(paths)) {
			methods := slices.Sorted(slices.Values(paths[path]))
			i := slices.IndexFunc(a.Rules, func(r presetAllowRule) bool { return slices.Equal(r.Methods, methods) })
			if i < 0 {
				a.Rules = append(a.Rules, presetAllowRule{Methods: methods})
				i = len(a.Rules) - 1
			}
			a.Rules[i].Paths = append(a.Rules[i].Paths, path)
		}
		out = append(out, a)
	}
	return out
}

func presetParamUnknown(rule, name string) error {
	return errCodeEn("preset_param_unknown", map[string]any{"rule": rule, "name": name},
		fmt.Sprintf("規則 %s: ${%s} という params はありません", rule, name), fmt.Sprintf("rule %s: no params named ${%s}", rule, name), nil)
}

// walkPresetStrings replaces the strings parameters go into: the hosts and
// the paths.
func walkPresetStrings(r *v1.NetworkPolicyRule, f func(string) string) {
	for i := range r.Endpoints {
		e := &r.Endpoints[i]
		e.Host, e.Path = f(e.Host), f(e.Path)
		for j := range e.Rules {
			if a := e.Rules[j].Allow; a != nil {
				x := *a
				x.Path = f(x.Path)
				e.Rules[j].Allow = &x
			}
		}
		for j := range e.DenyRules {
			e.DenyRules[j].Path = f(e.DenyRules[j].Path)
		}
	}
}

// checkParams: every parameter given, valid, and nothing else.
func (p *networkPreset) checkParams(params map[string]string) error {
	for name := range params {
		if _, ok := p.patterns[name]; !ok {
			return errCodeEn("preset_param_extra", map[string]any{"preset": p.Label, "name": name},
				fmt.Sprintf("%s に %s という指定はありません", p.Label, name), fmt.Sprintf("%s has no parameter %s", p.Label, name), nil)
		}
	}
	for _, d := range p.Params {
		v, ok := params[d.Name]
		if !ok || v == "" {
			return errCodeEn("preset_param_missing", map[string]any{"preset": p.Label, "param": d.Label},
				fmt.Sprintf("%s の「%s」を指定してください", p.Label, d.Label), fmt.Sprintf("%s: %q is required", p.Label, d.Label), nil)
		}
		if !presetParamValue.MatchString(v) || strings.Contains(v, "..") || slices.Contains(strings.Split(v, "/"), ".") || !p.patterns[d.Name].MatchString(v) {
			return errCodeEn("preset_param_value_invalid", map[string]any{"preset": p.Label, "param": d.Label, "value": v},
				fmt.Sprintf("%s の「%s」に %q は使えません", p.Label, d.Label, v), fmt.Sprintf("%s: %q cannot be used for %q", p.Label, v, d.Label), nil)
		}
	}
	return nil
}

// expand is the preset's rules with params in place, each checked as an
// edit's are.
func (p *networkPreset) expand(params map[string]string) (map[string]v1.NetworkPolicyRule, error) {
	if err := p.checkParams(params); err != nil {
		return nil, err
	}
	return p.build(params)
}

func (p *networkPreset) build(params map[string]string) (map[string]v1.NetworkPolicyRule, error) {
	sub := func(s string, name bool) string {
		return presetPlaceholder.ReplaceAllStringFunc(s, func(m string) string {
			v := params[m[2:len(m)-1]]
			if name {
				v = presetRuleNameChar.ReplaceAllString(v, "_")
			}
			return v
		})
	}
	out := map[string]v1.NetworkPolicyRule{}
	for key, r := range p.rules {
		r = cloneRule(r)
		name := sub(key, true)
		r.Name = name
		walkPresetStrings(&r, func(s string) string { return sub(s, false) })
		if !ruleNamePattern.MatchString(name) || isProviderRule(name) {
			return nil, errCodeEn("preset_rule_name_invalid", map[string]any{"rule": name},
				fmt.Sprintf("規則名 %s は英数字と _ . - の 100 文字までにし、_provider_ で始めないでください", name),
				fmt.Sprintf("rule name %s: up to 100 letters, digits, _ . -, not starting with _provider_", name), nil)
		}
		for _, e := range r.Endpoints {
			if !hostPattern.MatchString(e.Host) {
				return nil, errCodeEn("preset_host_invalid", map[string]any{"rule": name, "host": e.Host},
					fmt.Sprintf("規則 %s: ホスト %q は使えません", name, e.Host), fmt.Sprintf("rule %s: host %q is not valid", name, e.Host), nil)
			}
		}
		if _, dup := out[name]; dup {
			return nil, errCodeEn("preset_rule_name_duplicate", map[string]any{"rule": name},
				fmt.Sprintf("規則名 %s が重なっています", name), fmt.Sprintf("rule name %s appears twice", name), nil)
		}
		out[name] = r
	}
	return out, nil
}

// presetFitsProviders refuses rules that disagree with each other or with
// the initial rules of Iwato's providers (OpenShell's endpoint ambiguity).
func presetFitsProviders(rules map[string]v1.NetworkPolicyRule) error {
	if a := findEndpointAmbiguities(rules); len(a) > 0 {
		return errCodeEn("preset_rules_ambiguous", map[string]any{"detail": a[0].String()},
			"規則どうしで検査のしかたが食い違います: "+a[0].String(), "The rules disagree on how to inspect: "+a[0].String(), nil)
	}
	providersOnly := map[string]v1.NetworkPolicyRule{}
	for _, prof := range providerProfiles {
		add, err := providerInitialRules(prof.ID, "p")
		if err != nil {
			return err
		}
		for k, r := range add {
			providersOnly[prof.ID+"/"+k] = r
		}
	}
	all := cloneRules(providersOnly)
	maps.Copy(all, rules)
	if a := newAmbiguities(providersOnly, all); len(a) > 0 {
		return errCodeEn("preset_provider_ambiguous", map[string]any{"detail": a[0].String()},
			"プロバイダーの規則と検査のしかたが食い違います: "+a[0].String(), "The rules disagree with a provider's on how to inspect: "+a[0].String(), nil)
	}
	return nil
}

// presetList is Iwato's presets, then the owner's.
func presetList() ([]networkPreset, []presetFileError) {
	presetCatalog.RLock()
	defer presetCatalog.RUnlock()
	return append(slices.Clone(builtinPresets()), presetCatalog.user...), slices.Clone(presetCatalog.errors)
}

func findPreset(id string) *networkPreset {
	list, _ := presetList()
	i := slices.IndexFunc(list, func(p networkPreset) bool { return p.ID == id })
	if i < 0 {
		return nil
	}
	return &list[i]
}

// presetRefRules are one preset's rules.
func presetRefRules(ref PresetRef) (map[string]v1.NetworkPolicyRule, error) {
	p := findPreset(ref.ID)
	if p == nil {
		return nil, errCodeEn("preset_unknown", map[string]any{"id": ref.ID},
			fmt.Sprintf("%q という通信のプリセットはありません", ref.ID), fmt.Sprintf("No network preset %q", ref.ID), nil)
	}
	return p.expand(ref.Params)
}

// validPresetList checks the presets a create request names.
func validPresetList(refs []PresetRef) error {
	for i, ref := range refs {
		if _, err := presetRefRules(ref); err != nil {
			return err
		}
		if slices.ContainsFunc(refs[:i], ref.same) {
			return errCodeEn("preset_ref_duplicate", map[string]any{"preset": ref.label()},
				"通信のプリセット "+ref.label()+" が重なっています", "Network preset "+ref.label()+" appears twice", nil)
		}
	}
	return nil
}

// presetRules are the rules the presets add. The presets' rules of one
// name join: their programs and requests are the union. A preset that is
// gone (the owner's file removed, or one of Iwato's dropped in an update)
// adds nothing; the rules it put into a policy stay there as plain rules.
func presetRules(refs []PresetRef) map[string]v1.NetworkPolicyRule {
	out := map[string]v1.NetworkPolicyRule{}
	for _, ref := range refs {
		rules, err := presetRefRules(ref)
		if err != nil {
			log.Printf("preset %s: %v", ref.ID, english(err))
			continue
		}
		for _, name := range slices.Sorted(maps.Keys(rules)) {
			rule := rules[name]
			have, ok := out[name]
			if !ok {
				out[name] = rule
				continue
			}
			for _, b := range rule.Binaries {
				if !slices.ContainsFunc(have.Binaries, func(x v1.PolicyNetworkBinary) bool { return x.Path == b.Path }) {
					have.Binaries = append(have.Binaries, b)
				}
			}
			for _, e := range rule.Endpoints {
				i := slices.IndexFunc(have.Endpoints, func(x v1.PolicyNetworkEndpoint) bool { return endpointsOverlapForMerge(x, e) })
				if i < 0 {
					have.Endpoints = append(have.Endpoints, e)
					continue
				}
				for _, r := range e.Rules {
					have.Endpoints[i].Rules = appendUniqueAllow(have.Endpoints[i].Rules, r)
				}
			}
			out[name] = have
		}
	}
	return out
}

// withRequiredPresets is refs and the presets they require.
func withRequiredPresets(refs []PresetRef) []PresetRef {
	out := slices.Clone(refs)
	for i := 0; i < len(out); i++ {
		p := findPreset(out[i].ID)
		if p == nil {
			continue
		}
		for _, id := range p.Requires {
			if r := (PresetRef{ID: id}); !slices.ContainsFunc(out, r.same) {
				out = append(out, r)
			}
		}
	}
	return out
}

// presetView is what GET /api/presets answers: the presets, the owner's
// files that could not be read and the owner's folder.
type presetView struct {
	Presets []networkPreset   `json:"presets"`
	Errors  []presetFileError `json:"errors"`
	Folder  string            `json:"folder"`
}

// handlePresets serves GET /api/presets (the owner's folder read again).
func (s *Server) handlePresets(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, false) {
		return
	}
	if r.Method != http.MethodGet {
		fail(w, 405, "method not allowed")
		return
	}
	reloadUserPresets()
	list, errs := presetList()
	presetCatalog.RLock()
	dir := presetCatalog.dir
	presetCatalog.RUnlock()
	if errs == nil {
		errs = []presetFileError{}
	}
	jsonResponse(w, 200, presetView{Presets: list, Errors: errs, Folder: dir})
}
