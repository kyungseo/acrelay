package relay

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/platform"
	"github.com/kyungseo/acrelay/internal/store"
)

type CleanupMode string

const (
	CleanupList   CleanupMode = "list"
	CleanupDryRun CleanupMode = "dry-run"
	CleanupApply  CleanupMode = "apply"
)

// CleanupRequest is deliberately canonical/ref-scoped. It has no store-wide
// discovery or liveness mode.
type CleanupRequest struct {
	Canonical string
	Ref       string
	Mode      CleanupMode
	Actor     string
	Reason    string
}

// CleanupReport contains no native handle and uses basenames for sidecars so
// ordinary diagnostics do not expose private absolute paths.
type CleanupReport struct {
	Mode         CleanupMode
	Governance   string
	Ref          string
	Found        bool
	AlreadyClean bool
	Actions      []string
	Diagnostics  []string
	Blocked      []string
	Applied      bool
}

func validateCleanupRequest(req CleanupRequest) error {
	if strings.TrimSpace(req.Canonical) == "" || strings.TrimSpace(req.Ref) == "" {
		return fmt.Errorf("cleanup requires exact canonical and session_ref")
	}
	if req.Mode != CleanupList && req.Mode != CleanupDryRun && req.Mode != CleanupApply {
		return fmt.Errorf("cleanup mode must be list, dry-run, or apply")
	}
	if req.Mode == CleanupApply && (strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Reason) == "") {
		return fmt.Errorf("cleanup apply requires a declared actor and non-empty continuity-abandon reason")
	}
	return nil
}

func cleanupMarkerLabel(ref string) string { return "acrelay_cleanup " + ref }

func cleanupMarkerExists(canonical, ref string) (bool, error) {
	doc, err := store.ReadAll(canonical)
	if err != nil {
		return false, err
	}
	blocks, err := store.ListBlocks(doc)
	if err != nil {
		return false, err
	}
	for _, block := range blocks {
		if block.Label == cleanupMarkerLabel(ref) {
			if _, err := store.ExtractBlock(doc, block.Label); err != nil {
				return false, err
			}
			return true, nil
		}
	}
	return false, nil
}

func planCleanup(req CleanupRequest, handles *adapter.HandleStore) (*CleanupReport, *State, error) {
	if err := validateCleanupRequest(req); err != nil {
		return nil, nil, err
	}
	st, err := LoadState(req.Canonical)
	if err != nil {
		return nil, nil, err
	}
	if st == nil {
		return nil, nil, fmt.Errorf("cleanup refused: canonical has no objective state")
	}
	report := &CleanupReport{Mode: req.Mode, Governance: st.Governance, Ref: req.Ref}
	if signals, inspectErr := InspectPrivateLocation(req.Canonical); inspectErr != nil {
		report.Diagnostics = append(report.Diagnostics, "private-location diagnostic unavailable; absence of a signal is not a safety claim")
	} else if len(signals) > 0 {
		report.Diagnostics = append(report.Diagnostics, "canonical location warning: "+locationSignalKinds(signals)+"; cleanup remains non-blocking")
	}
	if st.SessionRef != req.Ref {
		report.Blocked = append(report.Blocked, "session_ref does not match the exact canonical-bound current reference")
		return report, st, nil
	}
	info, found, err := handles.Inspect(req.Ref)
	if err != nil {
		return nil, nil, err
	}
	report.Found = found
	if !found {
		report.AlreadyClean = true
		report.Diagnostics = append(report.Diagnostics, "handle mapping already absent; canonical remains owner-retained")
		return report, st, nil
	}
	if !kernel.IsGovTerminal(kernel.GovernanceState(st.Governance)) {
		report.Blocked = append(report.Blocked, "governance is nonterminal; OPEN/DECISION_REQUIRED/CLOSABLE cleanup is forbidden")
	}
	pending, err := pendingTransactions(req.Canonical)
	if err != nil {
		return nil, nil, err
	}
	if len(pending) > 0 {
		names := make([]string, 0, len(pending))
		for _, path := range pending {
			names = append(names, filepath.Base(path))
		}
		report.Blocked = append(report.Blocked, "pending dispatch journal(s) require reconcile or declared abandon: "+strings.Join(names, ","))
	}
	for _, tx := range st.Transactions {
		if tx.Result == "unknown" {
			report.Blocked = append(report.Blocked, "UNKNOWN transaction "+tx.ID+" requires resolution before cleanup")
		}
	}
	report.Actions = append(report.Actions, "remove exact handle mapping")
	if info.Neutral {
		report.Actions = append(report.Actions, "remove exact acrelay-owned neutral cwd")
		if info.LegacyTempRoot {
			report.Diagnostics = append(report.Diagnostics, "legacy temp-bound cwd; no automatic migration was attempted")
		}
		if info.WorkingDirGone {
			report.Diagnostics = append(report.Diagnostics, "neutral cwd is already missing; explicit session reset is required if continuity is resumed")
		}
	} else {
		report.Diagnostics = append(report.Diagnostics, "in-target cwd is user/subject-owned and will not be deleted")
	}
	return report, st, nil
}

// CleanupSession plans or applies exact session cleanup. Apply records the
// owner continuity-abandon declaration before touching the handle store/cwd.
// Raw canonical deletion is never an action of this API.
func CleanupSession(req CleanupRequest, handles *adapter.HandleStore) (*CleanupReport, error) {
	if handles == nil {
		return nil, fmt.Errorf("cleanup requires a handle store")
	}
	report, _, err := planCleanup(req, handles)
	if err != nil || req.Mode != CleanupApply || report.AlreadyClean {
		return report, err
	}
	if len(report.Blocked) > 0 {
		return report, nil
	}
	var applied *CleanupReport
	err = withCanonicalLock(req.Canonical, func() error {
		fresh, st, err := planCleanup(req, handles)
		if err != nil {
			return err
		}
		if fresh.AlreadyClean {
			applied = fresh
			return nil
		}
		if len(fresh.Blocked) > 0 {
			applied = fresh
			return nil
		}
		marked, err := cleanupMarkerExists(req.Canonical, req.Ref)
		if err != nil {
			return err
		}
		if !marked {
			rev, err := store.Revision(req.Canonical)
			if err != nil {
				return err
			}
			payload, err := json.Marshal(struct {
				Ref    string `json:"session_ref"`
				Actor  string `json:"actor"`
				Reason string `json:"continuity_abandon_reason"`
			}{req.Ref, strings.TrimSpace(req.Actor), strings.TrimSpace(req.Reason)})
			if err != nil {
				return err
			}
			header := fmt.Sprintf("\n## private session cleanup authorized\n- session_ref: %s\n- actor: %q\n- continuity: abandoned\n- reason: %q\n%s",
				req.Ref, req.Actor, req.Reason, store.EncodeBlock(cleanupMarkerLabel(req.Ref), payload))
			if err := appendState(req.Canonical, st, header, rev); err != nil {
				return err
			}
		}
		_, _, err = handles.Cleanup(req.Ref)
		if err != nil {
			return err
		}
		fresh.Applied = true
		applied = fresh
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applied, nil
}

// RenderCleanupReport is stable, bounded human output for the CLI.
func RenderCleanupReport(report *CleanupReport) string {
	if report == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cleanup mode=%s governance=%s ref=%s found=%v applied=%v already_clean=%v\n",
		report.Mode, report.Governance, report.Ref, report.Found, report.Applied, report.AlreadyClean)
	for _, action := range report.Actions {
		fmt.Fprintf(&b, "action: %s\n", action)
	}
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(&b, "diagnostic: %s\n", diagnostic)
	}
	for _, blocked := range report.Blocked {
		fmt.Fprintf(&b, "blocked: %s\n", blocked)
	}
	return b.String()
}

// SidecarCleanupRequest covers one exact quarantine or orphan sidecar. It
// never scans a directory or infers global liveness.
type SidecarCleanupRequest struct {
	Canonical string
	Path      string
	Mode      CleanupMode
	Actor     string
	Reason    string
}

type SidecarCleanupReport struct {
	Mode         CleanupMode
	Kind         string
	Name         string
	AlreadyClean bool
	Applied      bool
	Diagnostics  []string
	Blocked      []string
}

func validateSidecarRequest(req SidecarCleanupRequest) error {
	if strings.TrimSpace(req.Canonical) == "" || strings.TrimSpace(req.Path) == "" {
		return fmt.Errorf("sidecar cleanup requires exact canonical and sidecar path")
	}
	if req.Mode != CleanupList && req.Mode != CleanupDryRun && req.Mode != CleanupApply {
		return fmt.Errorf("cleanup mode must be list, dry-run, or apply")
	}
	if req.Mode == CleanupApply && (strings.TrimSpace(req.Actor) == "" || strings.TrimSpace(req.Reason) == "") {
		return fmt.Errorf("sidecar cleanup apply requires a declared actor and non-empty reason")
	}
	return nil
}

func quarantineIdentity(canonical, path string) (string, error) {
	prefix := canonical + ".quarantine-"
	if filepath.Dir(path) != filepath.Dir(canonical) || !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, ".json") {
		return "", fmt.Errorf("quarantine sidecar is not bound to the exact canonical: fail-closed")
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(path, prefix), ".json")
	cut := strings.LastIndex(rest, "-")
	if cut < 0 {
		return "", fmt.Errorf("quarantine filename lacks digest binding: fail-closed")
	}
	txID, digestPrefix := rest[:cut], rest[cut+1:]
	if !transactionIDPattern.MatchString(txID) || len(digestPrefix) != 12 {
		return "", fmt.Errorf("quarantine filename identity invalid: fail-closed")
	}
	return txID, nil
}

func quarantinePurgeMarker(txID string) string { return "acrelay_quarantine_purge " + txID }

// CleanupQuarantine purges one exact abandoned-transaction quarantine only
// after its full digest is present in canonical state. Apply appends purge
// actor/reason evidence before deletion; pending journals remain a blocker.
func CleanupQuarantine(req SidecarCleanupRequest) (*SidecarCleanupReport, error) {
	if err := validateSidecarRequest(req); err != nil {
		return nil, err
	}
	txID, err := quarantineIdentity(req.Canonical, req.Path)
	if err != nil {
		return nil, err
	}
	report := &SidecarCleanupReport{Mode: req.Mode, Kind: "quarantine", Name: filepath.Base(req.Path)}
	st, err := LoadState(req.Canonical)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, fmt.Errorf("quarantine cleanup requires the durable canonical disposition")
	}
	if _, err := os.Lstat(req.Path); os.IsNotExist(err) {
		report.AlreadyClean = true
		return report, nil
	} else if err != nil {
		return nil, err
	}
	if err := platform.VerifyPrivateFile(req.Path); err != nil {
		return nil, fmt.Errorf("quarantine sidecar is not private: %w", err)
	}
	raw, err := os.ReadFile(req.Path)
	if err != nil {
		return nil, err
	}
	digest := store.Digest(raw)
	disposed := false
	for _, tx := range st.Transactions {
		if tx.ID == txID && tx.Result == "abandoned" && tx.JournalDigest == digest {
			disposed = true
			break
		}
	}
	if !disposed {
		report.Blocked = append(report.Blocked, "canonical lacks the exact abandoned transaction digest disposition")
	}
	if pending, err := pendingTransactions(req.Canonical); err != nil {
		return nil, err
	} else if len(pending) > 0 {
		report.Blocked = append(report.Blocked, "pending transaction evidence must be resolved before quarantine purge")
	}
	report.Diagnostics = append(report.Diagnostics, "raw canonical remains owner-retained; secure deletion is not claimed")
	if req.Mode != CleanupApply || len(report.Blocked) > 0 {
		return report, nil
	}
	err = withCanonicalLock(req.Canonical, func() error {
		fresh, err := LoadState(req.Canonical)
		if err != nil {
			return err
		}
		if err := ensureNoPendingLocked(req.Canonical); err != nil {
			return err
		}
		stillDisposed := false
		for _, tx := range fresh.Transactions {
			if tx.ID == txID && tx.Result == "abandoned" && tx.JournalDigest == digest {
				stillDisposed = true
			}
		}
		if !stillDisposed {
			return fmt.Errorf("quarantine disposition changed before apply: fail-closed")
		}
		doc, err := store.ReadAll(req.Canonical)
		if err != nil {
			return err
		}
		marked := false
		if blocks, err := store.ListBlocks(doc); err != nil {
			return err
		} else {
			for _, block := range blocks {
				if block.Label == quarantinePurgeMarker(txID) {
					if _, err := store.ExtractBlock(doc, block.Label); err != nil {
						return err
					}
					marked = true
				}
			}
		}
		if !marked {
			rev, err := store.Revision(req.Canonical)
			if err != nil {
				return err
			}
			payload, err := json.Marshal(struct {
				TransactionID string `json:"transaction_id"`
				Digest        string `json:"journal_digest"`
				Actor         string `json:"actor"`
				Reason        string `json:"reason"`
			}{txID, digest, strings.TrimSpace(req.Actor), strings.TrimSpace(req.Reason)})
			if err != nil {
				return err
			}
			header := fmt.Sprintf("\n## private quarantine purge authorized\n- transaction_id: %s\n- actor: %q\n- reason: %q\n%s",
				txID, req.Actor, req.Reason, store.EncodeBlock(quarantinePurgeMarker(txID), payload))
			if err := appendState(req.Canonical, fresh, header, rev); err != nil {
				return err
			}
		}
		if err := store.RemovePrivate(req.Path); err != nil && !os.IsNotExist(err) {
			return err
		}
		report.Applied = true
		return nil
	})
	return report, err
}

func orphanSidecarKind(canonical, path string) (string, error) {
	if filepath.Dir(path) != filepath.Dir(canonical) {
		return "", fmt.Errorf("orphan sidecar is not beside the declared canonical: fail-closed")
	}
	base, canonicalBase := filepath.Base(path), filepath.Base(canonical)
	switch {
	case base == canonicalBase+".lock":
		return "lock", nil
	case strings.HasPrefix(base, canonicalBase+".dispatch-") && strings.HasSuffix(base, ".json"):
		txID := strings.TrimSuffix(strings.TrimPrefix(base, canonicalBase+".dispatch-"), ".json")
		if !transactionIDPattern.MatchString(txID) {
			return "", fmt.Errorf("orphan dispatch sidecar identity invalid: fail-closed")
		}
		return "dispatch", nil
	case strings.HasPrefix(base, canonicalBase+".quarantine-") && strings.HasSuffix(base, ".json"):
		if _, err := quarantineIdentity(canonical, path); err != nil {
			return "", err
		}
		return "quarantine", nil
	default:
		return "", fmt.Errorf("path is not a recognized exact canonical sidecar: fail-closed")
	}
}

// CleanupOrphanSidecar handles one exact sidecar only when the declared raw
// canonical is absent. No global orphan scan or reference-liveness claim is
// made. Lock deletion stays manual because safe inactivity cannot be proven
// from a missing canonical alone.
func CleanupOrphanSidecar(req SidecarCleanupRequest) (*SidecarCleanupReport, error) {
	if err := validateSidecarRequest(req); err != nil {
		return nil, err
	}
	kind, err := orphanSidecarKind(req.Canonical, req.Path)
	if err != nil {
		return nil, err
	}
	report := &SidecarCleanupReport{Mode: req.Mode, Kind: "orphan-" + kind, Name: filepath.Base(req.Path)}
	if _, err := os.Lstat(req.Canonical); err == nil {
		report.Blocked = append(report.Blocked, "canonical exists; use canonical-bound reconcile/quarantine cleanup")
		return report, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if _, err := os.Lstat(req.Path); os.IsNotExist(err) {
		report.AlreadyClean = true
		return report, nil
	} else if err != nil {
		return nil, err
	}
	if kind == "lock" {
		report.Blocked = append(report.Blocked, "lock inactivity cannot be proven from a missing canonical; coordinate manual removal")
		return report, nil
	}
	if err := platform.VerifyPrivateFile(req.Path); err != nil {
		return nil, fmt.Errorf("orphan sidecar is not private: %w", err)
	}
	report.Diagnostics = append(report.Diagnostics, "canonical is absent; owner declared this exact path abandoned; recovery and secure deletion are not claimed")
	if req.Mode != CleanupApply {
		return report, nil
	}
	if err := store.RemovePrivate(req.Path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	report.Applied = true
	return report, nil
}

func RenderSidecarCleanupReport(report *SidecarCleanupReport) string {
	if report == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "cleanup mode=%s kind=%s sidecar=%s applied=%v already_clean=%v\n",
		report.Mode, report.Kind, report.Name, report.Applied, report.AlreadyClean)
	for _, diagnostic := range report.Diagnostics {
		fmt.Fprintf(&b, "diagnostic: %s\n", diagnostic)
	}
	for _, blocked := range report.Blocked {
		fmt.Fprintf(&b, "blocked: %s\n", blocked)
	}
	return b.String()
}
