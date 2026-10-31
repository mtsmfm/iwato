package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"
	"strings"
	"time"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	dm "github.com/NVIDIA/OpenShell/sdk/go/proto/datamodelv1"
	pb "github.com/NVIDIA/OpenShell/sdk/go/proto/openshellv1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// profileAPI is the part of the gateway's API that keeps the provider
// profiles (pb.OpenShellClient; a fake in tests). The SDK's own profile
// types leave out the endpoints' access, rules and paths.
type profileAPI interface {
	GetProviderProfile(ctx context.Context, in *pb.GetProviderProfileRequest, opts ...grpc.CallOption) (*pb.ProviderProfileResponse, error)
	ImportProviderProfiles(ctx context.Context, in *pb.ImportProviderProfilesRequest, opts ...grpc.CallOption) (*pb.ImportProviderProfilesResponse, error)
	UpdateProviderProfiles(ctx context.Context, in *pb.UpdateProviderProfilesRequest, opts ...grpc.CallOption) (*pb.UpdateProviderProfilesResponse, error)
}

// ensureProfiles imports Iwato's profiles into the gateway (platform
// scope, as `openshell profile import --global`): the built-in ones, the
// scoped providers' and the custom ones (allProfiles), and replaces those
// an older Iwato (or another scope) imported. Profiles that are current
// are left alone.
func ensureProfiles(ctx context.Context, api profileAPI) error {
	var errs []error
	for _, p := range allProfiles() {
		if err := putProfile(ctx, api, importedProfile(p)); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// putProfile imports the profile (with its hash), or updates the
// gateway's copy when its hash differs.
func putProfile(ctx context.Context, api profileAPI, want *pb.ProviderProfile) error {
	id := want.Id
	got, err := api.GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: id})
	switch {
	case status.Code(err) == codes.NotFound:
		resp, err := api.ImportProviderProfiles(ctx, &pb.ImportProviderProfilesRequest{Profiles: []*pb.ProviderProfileImportItem{{Profile: want, Source: "iwato"}}})
		if err == nil && !resp.GetImported() {
			err = diagnosticsError(resp.GetDiagnostics())
		}
		if err != nil {
			return fmt.Errorf("import the provider profile %s: %w", id, err)
		}
		log.Printf("imported the provider profile %s", id)
	case err != nil:
		return fmt.Errorf("read the provider profile %s: %w", id, err)
	case got.GetProfile().GetAnnotations()[profileHashKey] != want.Annotations[profileHashKey]:
		resp, err := api.UpdateProviderProfiles(ctx, &pb.UpdateProviderProfilesRequest{
			Id: id, Profile: &pb.ProviderProfileImportItem{Profile: want, Source: "iwato"},
			ExpectedResourceVersion: got.GetProfile().GetResourceVersion(),
		})
		if err == nil && !resp.GetUpdated() {
			err = diagnosticsError(resp.GetDiagnostics())
		}
		if err != nil {
			return fmt.Errorf("update the provider profile %s: %w", id, err)
		}
		log.Printf("updated the provider profile %s", id)
	}
	return nil
}

// withHash is the profile with its hash annotation.
func withHash(m *pb.ProviderProfile) *pb.ProviderProfile {
	m = proto.Clone(m).(*pb.ProviderProfile)
	m.Annotations = map[string]string{profileHashKey: profileHash(m)}
	return m
}

// PutProviderProfile imports a scoped provider's or a custom profile, or
// replaces the gateway's copy. The gateway checks it against the sandboxes
// its providers are attached to and refuses it with its diagnostics.
func (o *OpenShell) PutProviderProfile(ctx context.Context, m *pb.ProviderProfile) error {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return err
	}
	api := o.profileAPI
	if api == nil {
		api = pb.NewOpenShellClient(conn)
	}
	return putProfile(ctx, api, withHash(m))
}

// DeleteProviderProfile deletes a profile Iwato imported (OpenShell
// refuses while a provider uses it); a missing one is fine.
func (o *OpenShell) DeleteProviderProfile(ctx context.Context, id string) error {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return err
	}
	_, err = pb.NewOpenShellClient(conn).DeleteProviderProfile(ctx, &pb.DeleteProviderProfileRequest{Id: id, AllowMissing: true})
	return err
}

// ObserveProvider asks the gateway to follow the running sandbox's current
// state of the provider (GetSandboxProviderStatus without a receipt
// records an observation of the current desired state) and returns the
// observation's receipt for ProviderReadiness.
func (o *OpenShell) ObserveProvider(ctx context.Context, sandbox, provider string) (string, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return "", err
	}
	resp, err := pb.NewOpenShellClient(conn).GetSandboxProviderStatus(ctx, &pb.GetSandboxProviderStatusRequest{WorkspaceScope: workspaceScope(), Sandbox: sandbox, Provider: provider})
	if err != nil {
		return "", err
	}
	return resp.GetStatus().GetReceipt().GetReceiptId(), nil
}

func diagnosticsError(ds []*pb.ProviderProfileDiagnostic) error {
	var msgs []string
	for _, d := range ds {
		if d.GetSeverity() != "warning" {
			msgs = append(msgs, strings.TrimPrefix(d.GetField()+": ", ": ")+d.GetMessage())
		}
	}
	if len(msgs) == 0 {
		msgs = append(msgs, "rejected")
	}
	return errors.New(strings.Join(msgs, "; "))
}

// ProviderProfile reads a profile as Iwato's workspace sees it (its own,
// else the platform's); nil when the gateway has none.
func (o *OpenShell) ProviderProfile(ctx context.Context, id string) (*pb.ProviderProfile, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := pb.NewOpenShellClient(conn).GetProviderProfile(ctx, &pb.GetProviderProfileRequest{Id: id, WorkspaceScope: workspaceScope()})
	if status.Code(err) == codes.NotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return resp.GetProfile(), nil
}

// profiles imports the profiles, again after a failure.
func (o *OpenShell) profiles(ctx context.Context) error {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return err
	}
	o.mu.Lock()
	done := o.profilesDone
	o.mu.Unlock()
	if done {
		return nil
	}
	api := o.profileAPI
	if api == nil {
		api = pb.NewOpenShellClient(conn)
	}
	if err := ensureProfiles(ctx, api); err != nil {
		return err
	}
	o.mu.Lock()
	o.profilesDone = true
	o.mu.Unlock()
	return nil
}

func workspaceScope() *dm.WorkspaceSelector {
	return &dm.WorkspaceSelector{Selection: &dm.WorkspaceSelector_Workspace{Workspace: openshellWorkspace}}
}

// ListProviders lists the providers of Iwato's workspace with their
// credentials' names (the gateway never returns the values), and the
// providers of each sandbox OpenShell has.
func (o *OpenShell) ListProviders(ctx context.Context) ([]ProviderInfo, map[string][]string, error) {
	sdk, conn, err := o.clients(ctx)
	if err != nil {
		return nil, nil, err
	}
	api := pb.NewOpenShellClient(conn)
	var list []ProviderInfo
	token := ""
	for {
		resp, err := api.ListProviders(ctx, &pb.ListProvidersRequest{WorkspaceScope: workspaceScope(), PageSize: 100, PageToken: token})
		if err != nil {
			return nil, nil, err
		}
		for _, p := range resp.GetProviders() {
			info := ProviderInfo{Name: p.GetMetadata().GetName(), Profile: p.GetType()}
			if t := p.GetMetadata().GetCreatedTime(); t != nil {
				info.CreatedAt = t.AsTime()
			}
			for k := range p.GetCredentials() {
				info.Keys = append(info.Keys, k)
			}
			for k := range p.GetCredentialHandles() {
				if !slices.Contains(info.Keys, k) {
					info.Keys = append(info.Keys, k)
				}
			}
			sort.Strings(info.Keys)
			list = append(list, info)
		}
		if token = resp.GetNextPageToken(); token == "" {
			break
		}
	}
	sandboxes, err := sdk.Sandboxes().ListAll(ctx, openshellWorkspace)
	if err != nil {
		return nil, nil, err
	}
	attached := map[string][]string{}
	for _, sb := range sandboxes {
		attached[sb.Name] = slices.Clone(sb.Spec.Providers)
	}
	return list, attached, nil
}

func (o *OpenShell) CreateProvider(ctx context.Context, name, profile string, credentials map[string]string) error {
	if err := o.profiles(ctx); err != nil {
		return errCodeEn("profile_import_failed", map[string]any{"detail": safeCLIError(err)}, "プロファイルを取り込めません: "+err.Error(), "cannot import the profile: "+english(err), err)
	}
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return err
	}
	_, err = sdk.Providers().Create(ctx, openshellWorkspace, &v1.Provider{Name: name, Type: profile, Spec: v1.ProviderSpec{Credentials: credentials}})
	return err
}

// UpdateProvider replaces the values (OpenShell merges by key; every key
// of the profile is given).
func (o *OpenShell) UpdateProvider(ctx context.Context, name string, credentials map[string]string) error {
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return err
	}
	p, err := sdk.Providers().Get(ctx, openshellWorkspace, name)
	if err != nil {
		return err
	}
	_, err = sdk.Providers().Update(ctx, openshellWorkspace, &v1.Provider{ID: p.ID, Name: name, Type: p.Type, ResourceVersion: p.ResourceVersion, Spec: v1.ProviderSpec{Credentials: credentials}})
	return err
}

func (o *OpenShell) DeleteProvider(ctx context.Context, name string) error {
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return err
	}
	_, err = sdk.Providers().Delete(ctx, openshellWorkspace, name)
	return err
}

// AttachProvider attaches the provider to a sandbox OpenShell has (false
// when it has none of that name). It returns as soon as the gateway took
// the change: a running sandbox applies it on its next settings poll
// (about 10 s), which ProviderReadiness follows with the receipt.
func (o *OpenShell) AttachProvider(ctx context.Context, sandbox, provider string) (bool, string, error) {
	return o.changeProvider(ctx, sandbox, provider, true)
}

// DetachProvider detaches the provider; the running sandbox stops
// resolving its placeholders when it applies the change.
func (o *OpenShell) DetachProvider(ctx context.Context, sandbox, provider string) (bool, string, error) {
	return o.changeProvider(ctx, sandbox, provider, false)
}

func (o *OpenShell) changeProvider(ctx context.Context, sandbox, provider string, attach bool) (bool, string, error) {
	sdk, conn, err := o.clients(ctx)
	if err != nil {
		return false, "", err
	}
	sb, err := sdk.Sandboxes().Get(ctx, openshellWorkspace, sandbox)
	if notFound(err) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	api := pb.NewOpenShellClient(conn)
	var receipt *pb.ProviderMutationReceipt
	if attach {
		resp, err := api.AttachSandboxProvider(ctx, &pb.AttachSandboxProviderRequest{WorkspaceScope: workspaceScope(), Sandbox: sandbox, Provider: provider})
		if err != nil {
			return false, "", err
		}
		receipt = resp.GetReceipt()
	} else {
		resp, err := api.DetachSandboxProvider(ctx, &pb.DetachSandboxProviderRequest{WorkspaceScope: workspaceScope(), Sandbox: sandbox, Provider: provider})
		if err != nil {
			return false, "", err
		}
		receipt = resp.GetReceipt()
	}
	// A stopped sandbox gets its providers when it starts: nothing to wait for.
	if sb.Status.Phase != v1.SandboxReady {
		return true, "", nil
	}
	o.writeTermEnv(ctx, sandbox)
	return true, receipt.GetReceiptId(), nil
}

// ProviderReadiness tells whether the running sandbox applied the change
// of the receipt: pending, ready, revoked, superseded (a later change
// replaced it), withheld or failed (with the reason), or gone when the
// gateway no longer knows the receipt.
func (o *OpenShell) ProviderReadiness(ctx context.Context, sandbox, provider, receipt string) (string, string, error) {
	_, conn, err := o.clients(ctx)
	if err != nil {
		return "", "", err
	}
	resp, err := pb.NewOpenShellClient(conn).GetSandboxProviderStatus(ctx, &pb.GetSandboxProviderStatusRequest{WorkspaceScope: workspaceScope(), Sandbox: sandbox, Provider: provider, ReceiptId: receipt})
	if status.Code(err) == codes.NotFound {
		return "gone", "", nil
	}
	if err != nil {
		return "", "", err
	}
	st := resp.GetStatus()
	reason := ""
	if st.GetReason() != pb.ProviderReadinessReason_PROVIDER_READINESS_REASON_UNSPECIFIED {
		reason = readinessName(st.GetReason())
	}
	return readinessName(st.GetState()), reason, nil
}

// readinessName is an enum value without its prefix (PENDING, ...).
func readinessName(v interface{ String() string }) string {
	s := v.String()
	for _, p := range []string{"PROVIDER_READINESS_STATE_", "PROVIDER_READINESS_REASON_"} {
		s = strings.TrimPrefix(s, p)
	}
	return strings.ToLower(s)
}

// The gateway's refresh of a provider credential (providers_refresh.go).

func (o *OpenShell) ConfigureRefresh(ctx context.Context, provider, key string, material map[string]string, secretKeys []string, expiresAt time.Time) error {
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return err
	}
	cfg := &v1.RefreshConfig{Provider: provider, CredentialKey: key, Strategy: v1.RefreshStrategyOAuth2RefreshToken, Material: material, SecretMaterialKeys: secretKeys}
	if !expiresAt.IsZero() {
		cfg.ExpiresAt = &expiresAt
	}
	_, err = sdk.Providers().Refresh().Configure(ctx, openshellWorkspace, cfg)
	return err
}

func (o *OpenShell) DeleteRefresh(ctx context.Context, provider, key string) error {
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return err
	}
	_, err = sdk.Providers().Refresh().Delete(ctx, openshellWorkspace, provider, key, v1.DeleteOptions{AllowMissing: true})
	if notFound(err) {
		return nil
	}
	return err
}

func (o *OpenShell) RotateRefresh(ctx context.Context, provider, key string) (RefreshInfo, error) {
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return RefreshInfo{}, err
	}
	st, err := sdk.Providers().Refresh().Rotate(ctx, openshellWorkspace, provider, key)
	if err != nil {
		return RefreshInfo{}, err
	}
	return refreshInfo(st), nil
}

func (o *OpenShell) RefreshStatus(ctx context.Context, provider, key string) (RefreshInfo, error) {
	sdk, _, err := o.clients(ctx)
	if err != nil {
		return RefreshInfo{}, err
	}
	list, err := sdk.Providers().Refresh().GetStatus(ctx, openshellWorkspace, provider, key)
	if notFound(err) {
		return RefreshInfo{Status: "not_configured"}, nil
	}
	if err != nil {
		return RefreshInfo{}, err
	}
	for _, st := range list {
		if st != nil && st.CredentialKey == key {
			return refreshInfo(st), nil
		}
	}
	return RefreshInfo{Status: "not_configured"}, nil
}

func refreshInfo(st *v1.RefreshStatus) RefreshInfo {
	if st == nil {
		return RefreshInfo{}
	}
	i := RefreshInfo{Status: st.Status, ExpiresAt: st.ExpiresAt, NextRefreshAt: st.NextRefreshAt, LastRefreshAt: st.LastRefreshAt, LastErrorAt: st.LastErrorAt,
		FailureCode: st.FailureCode, LastError: st.LastError}
	if a := st.RecoveryAction.String(); a != "unspecified" && a != "unknown" {
		i.Recovery = a
	}
	return i
}
