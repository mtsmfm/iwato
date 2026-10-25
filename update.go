package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Updating Iwato itself from GitHub Releases (docs/adr/0015). Each
// published build has iwato-build.json next to its binaries
// (scripts/build-meta.sh): the SHA-256 of each binary and the digests of
// its images. The exe checks its channel's file, downloads its own binary
// from the same release, checks the hash, swaps it in and restarts.

// buildChannel and buildRepo are set by scripts/build-exe.sh (--channel,
// --repo) for the published exes: edge follows the release `edge`, release
// the latest release of buildRepo (OWNER/REPO), pr-N the pre-release pr-N
// of a pull request (docs/adr/0019). Other builds never check.
var buildChannel, buildRepo string

// updateBaseOverride replaces https://github.com/OWNER/REPO/releases/ in
// tests (and, with the build tag uitest only, IWATO_UPDATE_BASE_URL).
var updateBaseOverride string

const (
	updateEvery      = 6 * time.Hour
	updateMetaName   = "iwato-build.json"
	updateMetaLimit  = 64 << 10
	updateAssetLimit = 512 << 20
	updateListLimit  = 8 << 20
	// updatePRMax: the pull request builds listed at most.
	updatePRMax = 20
	// restartListenEnv carries the listener's address to the restarted
	// exe, which waits for the port instead of picking another.
	restartListenEnv = "IWATO_RESTART_LISTEN"
	// restartRestoredEnv marks the old exe put back after a failed
	// restart, which must not roll back again.
	restartRestoredEnv = "IWATO_RESTART_RESTORED"
)

// buildMeta is iwato-build.json.
type buildMeta struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Built   string `json:"built"`
	Channel string `json:"channel"`
	// PR and Title: a pull request's build (channel pr); Commit is its head.
	PR     int               `json:"pr,omitempty"`
	Title  string            `json:"title,omitempty"`
	Images map[string]string `json:"images"`
	Assets map[string]string `json:"assets"`
}

var updateAssets = map[string]string{
	"windows/amd64": "iwato-windows-amd64.exe",
	"windows/arm64": "iwato-windows-arm64.exe",
	"darwin/arm64":  "iwato-darwin-arm64",
	"linux/amd64":   "iwato-linux-amd64",
	"linux/arm64":   "iwato-linux-arm64",
}

// assetName is the release's binary for goos/goarch ("" when there is none).
func assetName(goos, goarch string) string { return updateAssets[goos+"/"+goarch] }

var (
	semverPattern = regexp.MustCompile(`^(\d{1,9})\.(\d{1,9})\.(\d{1,9})(?:-([0-9A-Za-z.-]{1,32}))?$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	repoPattern   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	prTagPattern  = regexp.MustCompile(`^pr-([1-9][0-9]{0,8})$`)
)

// prOfTag is N for the tag pr-N, else 0.
func prOfTag(tag string) int {
	m := prTagPattern.FindStringSubmatch(tag)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// check validates m as the metadata of the release tag ("" for the
// latest release).
func (m buildMeta) check(tag string) error {
	channel, pr := tag, prOfTag(tag)
	switch {
	case tag == "":
		channel = "release"
	case pr > 0:
		channel = "pr"
	}
	if !semverPattern.MatchString(m.Version) || !commitPattern.MatchString(m.Commit) || m.Channel != channel || m.PR != pr || utf8.RuneCountInString(m.Title) > 300 {
		return errors.New("iwato-build.json: bad version, commit, channel or pull request")
	}
	for name, h := range m.Assets {
		if _, known := assetKnown(name); !known || !hashPattern.MatchString(h) {
			return fmt.Errorf("iwato-build.json: bad asset %q", name)
		}
	}
	for name, d := range m.Images {
		if (name != "iwato-gateway" && name != "iwato-sandbox") || !imageIDPattern.MatchString(d) {
			return fmt.Errorf("iwato-build.json: bad image %q", name)
		}
	}
	return nil
}

func assetKnown(name string) (string, bool) {
	for k, v := range updateAssets {
		if v == name {
			return k, true
		}
	}
	return "", false
}

// newerVersion reports whether semver a is newer than b (a release is
// newer than its pre-releases).
func newerVersion(a, b string) bool {
	ma, mb := semverPattern.FindStringSubmatch(a), semverPattern.FindStringSubmatch(b)
	if ma == nil {
		return false
	}
	if mb == nil {
		return true
	}
	for i := 1; i <= 3; i++ {
		x, _ := strconv.Atoi(ma[i])
		y, _ := strconv.Atoi(mb[i])
		if x != y {
			return x > y
		}
	}
	switch {
	case ma[4] == mb[4]:
		return false
	case ma[4] == "":
		return true
	case mb[4] == "":
		return false
	}
	return ma[4] > mb[4]
}

// newerBuild reports whether m is an update for the running build cur.
func newerBuild(channel string, m buildMeta, cur BuildInfo) bool {
	if channel != "release" {
		c := strings.TrimSuffix(cur.Commit, "-dirty")
		return c == "" || !strings.HasPrefix(m.Commit, c)
	}
	return newerVersion(m.Version, cur.Version)
}

// UpdateBuild is a build as the page shows it.
type UpdateBuild struct {
	Version string `json:"version"`
	Commit  string `json:"commit,omitempty"`
	Built   string `json:"built,omitempty"`
	PR      int    `json:"pr,omitempty"`
	Title   string `json:"title,omitempty"`
	// Gateway: installing it changes the gateway's image (lists only).
	Gateway bool `json:"gateway,omitempty"`
}

func updateBuildOf(m buildMeta) UpdateBuild {
	return UpdateBuild{Version: m.Version, Commit: m.Commit, Built: m.Built, PR: m.PR, Title: m.Title}
}

// UpdateView answers GET /api/update.
type UpdateView struct {
	Enabled bool         `json:"enabled"`
	Channel string       `json:"channel,omitempty"` // edge, release or pr
	Current UpdateBuild  `json:"current"`
	Latest  *UpdateBuild `json:"latest,omitempty"` // an update, when there is one
	// Switch: pull request builds can be installed (edge and pr).
	Switch bool `json:"switch,omitempty"`
	// Edge: the edge build a pull request's build can go back to; Gone:
	// that pull request's release is no longer published.
	Edge *UpdateBuild `json:"edge,omitempty"`
	Gone bool         `json:"gone,omitempty"`
	// Supported: the release has a binary for this OS and architecture.
	Supported bool `json:"supported"`
	// Gateway: the update changes the gateway's image, so OpenShell
	// restarts the running sandboxes (Restart).
	Gateway bool     `json:"gateway,omitempty"`
	Restart []string `json:"restart,omitempty"`
	Checked string   `json:"checked,omitempty"`
	// CheckFailed is the last check's failure (English, as logged).
	CheckFailed string `json:"checkFailed,omitempty"`
	// Phase: downloading, restarting or failed (Code, Params, Message).
	Phase   string         `json:"phase,omitempty"`
	Code    string         `json:"code,omitempty"`
	Params  map[string]any `json:"params,omitempty"`
	Message string         `json:"message,omitempty"`
}

// selfUpdate checks the channel and applies an update.
type selfUpdate struct {
	channel string // edge, release or pr
	pr      int    // channel pr: the pull request
	base    string // .../OWNER/REPO/releases/
	api     string // the releases' list in GitHub's API
	client  *http.Client
	// exe is the executable to replace (tests); empty: os.Executable.
	exe string
	// restart ends the server, which then starts the swapped exe (serve).
	restart func()
	// fetch, list and install are the network and the swap (the fake
	// replaces them). tag is the release: edge, pr-N, or "" for the latest
	// release.
	fetch   func(ctx context.Context, tag string) (buildMeta, error)
	list    func(ctx context.Context) ([]buildMeta, error)
	install func(ctx context.Context, tag string, m buildMeta) (string, error)

	mu          sync.Mutex
	current     BuildInfo
	latest      *buildMeta
	edge        *buildMeta // channel pr: the build to go back to
	gone        bool       // channel pr: its release is deleted
	title       string     // channel pr: the pull request's title
	checked     time.Time
	checkFailed string
	phase       string
	failure     error
	swapped     string // the exe swapped in, to start once the server ends
}

// newSelfUpdate is nil for builds without a channel.
func newSelfUpdate() *selfUpdate {
	channel, pr := buildChannel, prOfTag(buildChannel)
	if pr > 0 {
		channel = "pr"
	}
	if (channel != "edge" && channel != "release" && channel != "pr") || !repoPattern.MatchString(buildRepo) {
		return nil
	}
	base, api := "https://github.com/"+buildRepo+"/releases/", "https://api.github.com/repos/"+buildRepo+"/releases?per_page=100"
	if updateBaseOverride != "" {
		base, api = updateBaseOverride, updateBaseOverride+"list"
	}
	u := &selfUpdate{channel: channel, pr: pr, base: base, api: api, current: currentBuild()}
	u.client = &http.Client{CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		if !u.allowed(r.URL) {
			return fmt.Errorf("redirect to %s refused", r.URL.Host)
		}
		return nil
	}}
	u.fetch = u.fetchMeta
	u.list = u.listPRs
	u.install = u.download
	return u
}

// tag is the release this build follows ("" for the latest release).
func (u *selfUpdate) tag() string {
	switch u.channel {
	case "edge":
		return "edge"
	case "pr":
		return "pr-" + strconv.Itoa(u.pr)
	}
	return ""
}

// allowed: only https from GitHub (or the test server).
func (u *selfUpdate) allowed(target *url.URL) bool {
	if updateBaseOverride != "" {
		b, err := url.Parse(updateBaseOverride)
		return err == nil && b.Scheme == target.Scheme && b.Host == target.Host
	}
	h := target.Hostname()
	return target.Scheme == "https" && (h == "github.com" || h == "api.github.com" || strings.HasSuffix(h, ".githubusercontent.com"))
}

func (u *selfUpdate) metaURL(tag string) string {
	if tag == "" {
		return u.base + "latest/download/" + updateMetaName
	}
	return u.base + "download/" + tag + "/" + updateMetaName
}

func (u *selfUpdate) assetURL(tag string, m buildMeta, asset string) string {
	if tag == "" {
		tag = "v" + m.Version
	}
	return u.base + "download/" + tag + "/" + asset
}

// errReleaseGone: the release has no such file (a pull request's release
// is deleted when it is closed).
var errReleaseGone = errors.New("not published")

// get fetches target into w, at most limit bytes.
func (u *selfUpdate) get(ctx context.Context, target string, w io.Writer, limit int64) error {
	t, err := url.Parse(target)
	if err != nil || !u.allowed(t) {
		return fmt.Errorf("refused URL %s", target)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	res, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("GET %s: %w", target, errReleaseGone)
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", target, res.Status)
	}
	if res.ContentLength > limit {
		return fmt.Errorf("GET %s: too large", target)
	}
	n, err := io.Copy(w, io.LimitReader(res.Body, limit+1))
	if err == nil && n > limit {
		err = fmt.Errorf("GET %s: too large", target)
	}
	return err
}

func (u *selfUpdate) fetchMeta(ctx context.Context, tag string) (buildMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var b strings.Builder
	if err := u.get(ctx, u.metaURL(tag), &b, updateMetaLimit); err != nil {
		return buildMeta{}, err
	}
	var m buildMeta
	if err := json.Unmarshal([]byte(b.String()), &m); err != nil {
		return buildMeta{}, fmt.Errorf("iwato-build.json: %w", err)
	}
	return m, m.check(tag)
}

// listPRs fetches the metadata of the pull requests' pre-releases pr-N,
// newest pull request first.
func (u *selfUpdate) listPRs(ctx context.Context) ([]buildMeta, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var b strings.Builder
	if err := u.get(ctx, u.api, &b, updateListLimit); err != nil {
		return nil, err
	}
	type asset struct {
		Name string `json:"name"`
	}
	var releases []struct {
		Tag        string  `json:"tag_name"`
		Draft      bool    `json:"draft"`
		Prerelease bool    `json:"prerelease"`
		Assets     []asset `json:"assets"`
	}
	if err := json.Unmarshal([]byte(b.String()), &releases); err != nil {
		return nil, fmt.Errorf("releases: %w", err)
	}
	var tags []string
	for _, r := range releases {
		if prOfTag(r.Tag) > 0 && r.Prerelease && !r.Draft && slices.Contains(r.Assets, asset{updateMetaName}) {
			tags = append(tags, r.Tag)
		}
	}
	slices.SortFunc(tags, func(a, b string) int { return cmp.Compare(prOfTag(b), prOfTag(a)) })
	tags = tags[:min(len(tags), updatePRMax)]
	metas := make([]*buildMeta, len(tags))
	var wg sync.WaitGroup
	for i, tag := range tags {
		wg.Go(func() {
			m, err := u.fetchMeta(ctx, tag)
			if err != nil {
				log.Printf("pull request builds: %s: %v", tag, err)
				return
			}
			metas[i] = &m
		})
	}
	wg.Wait()
	var out []buildMeta
	for _, m := range metas {
		if m != nil {
			out = append(out, *m)
		}
	}
	return out, nil
}

// check fetches the channel's metadata now (a pull request's build:
// edge's too, to go back to).
func (u *selfUpdate) check(ctx context.Context) error {
	m, err := u.fetch(ctx, u.tag())
	var edge *buildMeta
	if u.channel == "pr" {
		if e, eerr := u.fetch(ctx, "edge"); eerr == nil {
			edge = &e
		}
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.checked = time.Now().UTC()
	u.edge, u.gone = edge, false
	if u.channel == "pr" && errors.Is(err, errReleaseGone) {
		u.gone, u.latest, u.checkFailed = true, nil, ""
		return nil
	}
	if err != nil {
		u.checkFailed = err.Error()
		log.Printf("update check: %v", err)
		return err
	}
	u.checkFailed = ""
	if u.channel == "pr" {
		u.title = m.Title
	}
	if newerBuild(u.channel, m, u.current) {
		if u.latest == nil || u.latest.Commit != m.Commit {
			log.Printf("update available: %s %s", m.Version, m.Commit)
		}
		u.latest = &m
	} else {
		u.latest = nil
	}
	return nil
}

// loop checks at startup and then every updateEvery.
func (u *selfUpdate) loop(ctx context.Context) {
	for {
		_ = u.check(ctx)
		select {
		case <-ctx.Done():
			return
		case <-time.After(updateEvery):
		}
	}
}

// available is the update, when there is one.
func (u *selfUpdate) available() *buildMeta {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.latest == nil {
		return nil
	}
	m := *u.latest
	return &m
}

func (u *selfUpdate) currentBuild() BuildInfo {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.current
}

func (u *selfUpdate) view() UpdateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := UpdateView{Enabled: true, Channel: u.channel, Current: UpdateBuild{Version: u.current.Version, Commit: u.current.Commit, Built: u.current.Built, PR: u.pr, Title: u.title},
		Supported: assetName(runtime.GOOS, runtime.GOARCH) != "", CheckFailed: u.checkFailed, Phase: u.phase,
		Switch: u.channel != "release", Gone: u.gone}
	if !u.checked.IsZero() {
		v.Checked = u.checked.Format(time.RFC3339)
	}
	if m := u.latest; m != nil {
		b := updateBuildOf(*m)
		v.Latest = &b
	}
	if m := u.edge; m != nil {
		b := updateBuildOf(*m)
		v.Edge = &b
	}
	if u.failure != nil {
		v.Message = u.failure.Error()
		if c := (*codedError)(nil); errors.As(u.failure, &c) && c.msg == u.failure.Error() {
			v.Code, v.Params = c.code, c.params
		}
	}
	return v
}

// start applies the update the owner confirmed (its commit) in the
// background; the page polls the view.
func (u *selfUpdate) start(ctx context.Context, commit string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.phase == "downloading" || u.phase == "restarting":
		return errCode("update_busy", nil, "更新しています")
	case u.latest == nil:
		return errCode("update_none", nil, "更新はありません")
	case u.latest.Commit != commit:
		return errCode("update_changed", nil, "新しい版が変わりました。確認し直してください")
	case assetName(runtime.GOOS, runtime.GOARCH) == "":
		return errCode("update_unsupported", nil, "この OS 向けの更新はありません")
	}
	u.phase, u.failure = "downloading", nil
	go u.apply(ctx, u.tag(), commit)
	return nil
}

// switchTo installs the build of another release (edge or pr-N) that the
// owner confirmed, newer or not. Release builds stay on releases.
func (u *selfUpdate) switchTo(ctx context.Context, tag, commit string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	switch {
	case u.channel == "release":
		return errCode("update_switch_unavailable", nil, "リリース版は他のビルドに切り替えられません")
	case tag != "edge" && prOfTag(tag) == 0, !commitPattern.MatchString(commit):
		return errCode("update_switch_bad", nil, "切り替え先のビルドが正しくありません")
	case u.phase == "downloading" || u.phase == "restarting":
		return errCode("update_busy", nil, "更新しています")
	case assetName(runtime.GOOS, runtime.GOARCH) == "":
		return errCode("update_unsupported", nil, "この OS 向けの更新はありません")
	}
	u.phase, u.failure = "downloading", nil
	go u.apply(ctx, tag, commit)
	return nil
}

func (u *selfUpdate) apply(ctx context.Context, tag, commit string) {
	fail := func(err error) {
		log.Printf("update failed: %v", english(err))
		u.mu.Lock()
		u.phase, u.failure = "failed", err
		u.mu.Unlock()
	}
	// The release may have moved since the owner confirmed (edge, pr-N).
	m, err := u.fetch(ctx, tag)
	if err != nil {
		fail(errCode("update_download", map[string]any{"detail": err.Error()}, "ダウンロードできませんでした: "+err.Error()))
		return
	}
	if m.Commit != commit {
		if tag == u.tag() {
			u.mu.Lock()
			if newerBuild(u.channel, m, u.current) {
				u.latest = &m
			} else {
				u.latest = nil
			}
			u.mu.Unlock()
		}
		fail(errCode("update_changed", nil, "新しい版が変わりました。確認し直してください"))
		return
	}
	exe, err := u.install(ctx, tag, m)
	if err != nil {
		fail(err)
		return
	}
	log.Printf("updated %s to %s %s: restarting", exe, m.Version, m.Commit)
	u.mu.Lock()
	u.phase, u.swapped = "restarting", exe
	u.mu.Unlock()
	if u.restart != nil {
		u.restart()
	}
}

// swappedExe is the exe swapped in, once the server should start it.
func (u *selfUpdate) swappedExe() string {
	if u == nil {
		return ""
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.swapped
}

// download fetches this OS's binary from m's release next to the
// executable, checks its hash and that it runs, and swaps it in. A failure
// before the swap changes nothing.
func (u *selfUpdate) download(ctx context.Context, tag string, m buildMeta) (string, error) {
	asset := assetName(runtime.GOOS, runtime.GOARCH)
	want := m.Assets[asset]
	if want == "" {
		return "", errCode("update_unsupported", nil, "この OS 向けの更新はありません")
	}
	exe := u.exe
	if exe == "" {
		var err error
		if exe, err = selfExe(); err != nil {
			return "", errCode("update_swap", map[string]any{"detail": err.Error()}, "入れ替えられませんでした: "+err.Error())
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(exe), ".iwato-update-*"+filepath.Ext(exe))
	if err != nil {
		return "", errCode("update_swap", map[string]any{"detail": err.Error()}, "入れ替えられませんでした: "+err.Error())
	}
	done := false
	defer func() {
		if !done {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	dctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	h := sha256.New()
	if err := u.get(dctx, u.assetURL(tag, m, asset), io.MultiWriter(tmp, h), updateAssetLimit); err != nil {
		return "", errCode("update_download", map[string]any{"detail": err.Error()}, "ダウンロードできませんでした: "+err.Error())
	}
	if err := tmp.Close(); err != nil {
		return "", errCode("update_download", map[string]any{"detail": err.Error()}, "ダウンロードできませんでした: "+err.Error())
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", errCode("update_hash", map[string]any{"asset": asset}, "ダウンロードした "+asset+" のハッシュが iwato-build.json と違います")
	}
	if err := prepareExe(exe, tmp.Name()); err != nil {
		return "", errCode("update_swap", map[string]any{"detail": err.Error()}, "入れ替えられませんでした: "+err.Error())
	}
	if err := checkRuns(ctx, tmp.Name(), m); err != nil {
		return "", errCode("update_verify", map[string]any{"detail": err.Error()}, "新しい Iwato を起動できませんでした: "+err.Error())
	}
	if err := swapExe(exe, tmp.Name()); err != nil {
		return "", errCode("update_swap", map[string]any{"detail": err.Error()}, "入れ替えられませんでした: "+err.Error())
	}
	done = true
	return exe, nil
}

// prepareExe gives the new file the old one's mode.
func prepareExe(exe, next string) error {
	st, err := os.Stat(exe)
	if err != nil {
		return err
	}
	if err := os.Chmod(next, st.Mode().Perm()|0o100); err != nil {
		return err
	}
	clearQuarantine(next)
	return nil
}

// checkRuns runs `next version` and expects m's version and commit.
func checkRuns(ctx context.Context, next string, m buildMeta) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, next, "version")
	configureProcess(c)
	out, err := c.Output()
	if err != nil {
		return err
	}
	f := strings.Fields(string(out))
	if len(f) < 3 || f[0] != "iwato" || f[1] != m.Version || len(f[2]) < 7 || !strings.HasPrefix(m.Commit, f[2]) {
		return fmt.Errorf("it reports %q", strings.TrimSpace(string(out)))
	}
	return nil
}

// swapExe puts next in place of exe, keeping the old one as exe.old until
// the next start (removeOldExe) or a restore (restoreExe). Windows lets a
// running exe be renamed, not replaced; elsewhere the rename over it is
// atomic and the old one stays as a hard link.
func swapExe(exe, next string) error {
	old := exe + ".old"
	if err := os.Remove(old); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if runtime.GOOS == "windows" {
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(next, exe); err != nil {
			_ = os.Rename(old, exe)
			return err
		}
		return nil
	}
	if err := os.Link(exe, old); err != nil {
		if err := copyFile(exe, old); err != nil {
			return err
		}
	}
	return os.Rename(next, exe)
}

func restoreExe(exe string) error { return os.Rename(exe+".old", exe) }

var selfExe = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

func restartEnv(addr string) []string {
	env := []string{restartListenEnv + "=" + addr}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, restartListenEnv+"=") {
			env = append(env, kv)
		}
	}
	return env
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// removeOldExe deletes the exe an update replaced, once serving: until
// then a failed start puts it back. On Windows the old process may still
// run it for a moment.
func removeOldExe() {
	exe, err := selfExe()
	if err != nil {
		return
	}
	for range 30 {
		if err := os.Remove(exe + ".old"); err == nil || errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(2 * time.Second)
	}
}

// handleUpdate answers GET /api/update and /api/update/prs (the pull
// requests' builds), and POST /api/update/check, /api/update/apply and
// /api/update/switch (to edge or a pull request's build).
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	action := strings.TrimPrefix(r.URL.Path, "/api/update")
	get := action == "" || action == "/prs"
	switch {
	case !get && action != "/check" && action != "/apply" && action != "/switch":
		fail(w, 404, "not found")
		return
	case get && r.Method != http.MethodGet, !get && r.Method != http.MethodPost:
		fail(w, 405, "method not allowed")
		return
	}
	if !s.require(w, r, !get) {
		return
	}
	u := s.update
	if u == nil {
		if action != "" {
			failCode(w, 409, "update_unavailable", nil, "この版は更新を確認しません")
			return
		}
		b := currentBuild()
		jsonResponse(w, 200, UpdateView{Current: UpdateBuild{Version: b.Version, Commit: b.Commit, Built: b.Built}})
		return
	}
	switch action {
	case "/prs":
		s.handleUpdatePRs(w, r, u)
		return
	case "/check":
		ctx, cancel := timeout(r.Context(), 40*time.Second)
		defer cancel()
		_ = u.check(ctx)
	case "/apply", "/switch":
		var body struct {
			Tag    string `json:"tag"`
			Commit string `json:"commit"`
		}
		if decode(w, r, &body) != nil {
			fail(w, 400, "invalid request")
			return
		}
		var err error
		if action == "/switch" {
			err = u.switchTo(s.ctx, body.Tag, body.Commit)
		} else {
			err = u.start(s.ctx, body.Commit)
		}
		if err != nil {
			failErr(w, 409, err)
			return
		}
	}
	v := u.view()
	m, e := u.available(), u.edgeBuild()
	if v.Phase == "" && (m != nil || e != nil) {
		ctx, cancel := timeout(r.Context(), 15*time.Second)
		defer cancel()
		now := s.gatewayNow(ctx)
		if m != nil {
			v.Gateway = gatewayChanges(now, *m)
		}
		if e != nil && v.Edge != nil {
			v.Edge.Gateway = gatewayChanges(now, *e)
		}
		if v.Gateway || (v.Edge != nil && v.Edge.Gateway) {
			v.Restart = s.runningSandboxes(ctx)
		}
	}
	jsonResponse(w, 200, v)
}

// UpdatePRs answers GET /api/update/prs.
type UpdatePRs struct {
	Builds  []UpdateBuild `json:"builds"`
	Restart []string      `json:"restart,omitempty"`
}

func (s *Server) handleUpdatePRs(w http.ResponseWriter, r *http.Request, u *selfUpdate) {
	if u.channel == "release" {
		failCode(w, 409, "update_switch_unavailable", nil, "リリース版は他のビルドに切り替えられません")
		return
	}
	ctx, cancel := timeout(r.Context(), 40*time.Second)
	defer cancel()
	metas, err := u.list(ctx)
	if err != nil {
		failCode(w, 502, "update_list", map[string]any{"detail": err.Error()}, "PR のビルドを取得できませんでした: "+err.Error())
		return
	}
	now := s.gatewayNow(ctx)
	out := UpdatePRs{Builds: []UpdateBuild{}}
	gateway := false
	for _, m := range metas {
		b := updateBuildOf(m)
		b.Gateway = gatewayChanges(now, m)
		gateway = gateway || b.Gateway
		out.Builds = append(out.Builds, b)
	}
	if gateway {
		out.Restart = s.runningSandboxes(ctx)
	}
	jsonResponse(w, 200, out)
}

func (u *selfUpdate) edgeBuild() *buildMeta {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.edge == nil {
		return nil
	}
	m := *u.edge
	return &m
}

// gatewayImager is a runtime that can tell the gateway container's image.
type gatewayImager interface {
	GatewayImage(ctx context.Context) string
}

// gatewayNow is the digest of the gateway container's image (or, when
// that is unknown, this exe's), "" when unknown.
func (s *Server) gatewayNow(ctx context.Context) string {
	now := ""
	if g, ok := s.rt.(gatewayImager); ok {
		now = imageDigest(g.GatewayImage(ctx))
	}
	if now == "" && pullsGatewayImage() {
		now = imageDigest(imageRef("iwato-gateway"))
	}
	return now
}

// gatewayChanges reports whether m's gateway image differs from now
// (gatewayNow). OpenShell restarts the running sandboxes when the gateway
// is recreated.
func gatewayChanges(now string, m buildMeta) bool {
	next := m.Images["iwato-gateway"]
	return now == "" || next == "" || now != next
}

func imageDigest(ref string) string {
	if _, d, ok := strings.Cut(ref, "@"); ok && imageIDPattern.MatchString(d) {
		return d
	}
	return ""
}

func (s *Server) runningSandboxes(ctx context.Context) []string {
	var names []string
	for _, sb := range s.sandboxList() {
		if s.rt.Status(ctx, sb).State == "running" {
			names = append(names, sb.Name)
		}
	}
	return names
}
