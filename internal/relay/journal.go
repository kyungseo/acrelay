package relay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kyungseo/acrelay/internal/adapter"
	"github.com/kyungseo/acrelay/internal/kernel"
	"github.com/kyungseo/acrelay/internal/review"
	"github.com/kyungseo/acrelay/internal/store"
)

const (
	dispatchJournalVersion = "dispatch-journal v0.1"
	journalPhasePrepared   = "prepared"
	journalPhaseCaptured   = "captured"
	journalPhaseUnknown    = "unknown"
)

var transactionIDPattern = regexp.MustCompile(`^tx-[0-9a-f]{32}$`)

// beforeJournalCleanup is a deterministic crash-window hook used only by
// package tests. Production leaves it nil.
var beforeJournalCleanup func(path string) error

// beforeCanonicalAppend is the companion subprocess crash hook for the
// captured-result→canonical-append window. Production leaves it nil.
var beforeCanonicalAppend func(path string)

func removeDispatchJournal(path string) error {
	if beforeJournalCleanup != nil {
		if err := beforeJournalCleanup(path); err != nil {
			return err
		}
	}
	return store.RemovePrivate(path)
}

// dispatchJournal is the private write-ahead record that bridges child start
// and canonical append. Prepared means execution is ambiguous after a host
// crash; captured/unknown carry the exact section that reconcile may append.
type dispatchJournal struct {
	Version          string `json:"version"`
	TransactionID    string `json:"transaction_id"`
	Kind             string `json:"kind"` // review | confirmation
	CollaborationID  string `json:"collaboration_id"`
	ObjectiveID      string `json:"objective_id"`
	StateSeq         int    `json:"state_seq"`
	ExpectedStateSeq int    `json:"expected_state_seq"`
	RoundIndex       int    `json:"round_index"`
	AttemptIndex     int    `json:"attempt_index"`
	Reviewer         string `json:"reviewer"`
	PreSnapshot      string `json:"pre_snapshot"`
	TargetRevision   string `json:"target_revision"`
	Phase            string `json:"phase"`
	PayloadDigest    string `json:"payload_digest,omitempty"`
	Section          string `json:"section_base64,omitempty"`
	CreatedAt        string `json:"created_at"`
}

type transactionMarker struct {
	TransactionID string `json:"transaction_id"`
	Kind          string `json:"kind"`
	PayloadDigest string `json:"payload_digest"`
}

func newTransactionID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tx-" + hex.EncodeToString(b), nil
}

func dispatchJournalPath(canonical, transactionID string) string {
	return canonical + ".dispatch-" + transactionID + ".json"
}

func listCanonicalSidecars(canonical, infix, suffix string) ([]string, error) {
	dir := filepath.Dir(canonical)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	prefix := filepath.Base(canonical) + infix
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), suffix) {
			continue
		}
		paths = append(paths, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths, nil
}

func pendingDispatchJournals(canonical string) ([]string, error) {
	return listCanonicalSidecars(canonical, ".dispatch-", ".json")
}

func pendingTransactions(canonical string) ([]string, error) {
	return pendingDispatchJournals(canonical)
}

func ensureNoPendingLocked(canonical string) error {
	pending, err := pendingTransactions(canonical)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return fmt.Errorf("pending transactions %v: status, reconcile, or declared abandon required before mutation", pending)
	}
	return nil
}

func writeDispatchJournal(path string, j *dispatchJournal) (string, error) {
	b, err := json.MarshalIndent(j, "", " ")
	if err != nil {
		return "", err
	}
	return store.WritePrivateAtomic(path, b)
}

// createDispatchJournalLocked must be called while holding the canonical
// flock after ensureNoPendingLocked. This makes guard-check→journal-create
// one cross-process critical section; the lock is released before dispatch.
func createDispatchJournalLocked(canonical string, st *State, kind string, roundIndex, attemptIndex int, reviewer, preSnapshot string) (*dispatchJournal, string, string, error) {
	if err := ensureNoPendingLocked(canonical); err != nil {
		return nil, "", "", err
	}
	txID, err := newTransactionID()
	if err != nil {
		return nil, "", "", err
	}
	j := &dispatchJournal{
		Version: dispatchJournalVersion, TransactionID: txID, Kind: kind,
		CollaborationID: st.CollaborationID, ObjectiveID: st.ObjectiveID,
		StateSeq: st.Seq, ExpectedStateSeq: st.Seq + 1,
		RoundIndex: roundIndex, AttemptIndex: attemptIndex, Reviewer: reviewer,
		PreSnapshot: preSnapshot, TargetRevision: st.TargetRevision,
		Phase: journalPhasePrepared, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	path := dispatchJournalPath(canonical, txID)
	if _, err := os.Lstat(path); err == nil {
		return nil, "", "", fmt.Errorf("dispatch journal collision at %s: fail-closed", path)
	} else if !os.IsNotExist(err) {
		return nil, "", "", err
	}
	diagnostic, err := writeDispatchJournal(path, j)
	if err != nil {
		return nil, "", diagnostic, err
	}
	return j, path, diagnostic, nil
}

func loadDispatchJournal(canonical, path string) (*dispatchJournal, []byte, error) {
	prefix := canonical + ".dispatch-"
	if filepath.Dir(path) != filepath.Dir(canonical) || !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, ".json") {
		return nil, nil, fmt.Errorf("dispatch journal %s is not bound to canonical %s: fail-closed", path, canonical)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, nil, fmt.Errorf("dispatch journal %s permission %o exposes group/other: fail-closed", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var j dispatchJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return nil, b, fmt.Errorf("dispatch journal corrupt: fail-closed: %w", err)
	}
	if j.Version != dispatchJournalVersion || !transactionIDPattern.MatchString(j.TransactionID) {
		return nil, b, fmt.Errorf("dispatch journal version/transaction identity invalid: fail-closed")
	}
	if filepath.Base(path) != filepath.Base(dispatchJournalPath(canonical, j.TransactionID)) {
		return nil, b, fmt.Errorf("dispatch journal filename does not match embedded transaction identity: fail-closed")
	}
	if j.Kind != "review" && j.Kind != "confirmation" {
		return nil, b, fmt.Errorf("dispatch journal kind %q invalid: fail-closed", j.Kind)
	}
	if j.Phase != journalPhasePrepared && j.Phase != journalPhaseCaptured && j.Phase != journalPhaseUnknown {
		return nil, b, fmt.Errorf("dispatch journal phase %q invalid: fail-closed", j.Phase)
	}
	return &j, b, nil
}

func setJournalSection(path string, j *dispatchJournal, phase, payload string) (string, error) {
	if phase != journalPhaseCaptured && phase != journalPhaseUnknown {
		return "", fmt.Errorf("journal section phase %q invalid", phase)
	}
	payloadDigest := store.Digest([]byte(payload))
	markerBytes, err := json.Marshal(transactionMarker{
		TransactionID: j.TransactionID, Kind: j.Kind, PayloadDigest: payloadDigest,
	})
	if err != nil {
		return "", err
	}
	full := payload + store.EncodeBlock("acrelay_transaction "+j.TransactionID, markerBytes)
	j.Phase, j.PayloadDigest = phase, payloadDigest
	j.Section = base64.StdEncoding.EncodeToString([]byte(full))
	return writeDispatchJournal(path, j)
}

func decodeJournalSection(j *dispatchJournal) ([]byte, error) {
	if j.Section == "" || !hex64.MatchString(j.PayloadDigest) {
		return nil, fmt.Errorf("dispatch journal %s has no valid captured section: fail-closed", j.TransactionID)
	}
	section, err := base64.StdEncoding.DecodeString(j.Section)
	if err != nil {
		return nil, fmt.Errorf("dispatch journal section base64: fail-closed: %w", err)
	}
	return section, nil
}

func findTransactionMarker(doc, txID string) (*transactionMarker, error) {
	blocks, err := store.ListBlocks(doc)
	if err != nil {
		return nil, err
	}
	label := "acrelay_transaction " + txID
	var found []store.Block
	for _, b := range blocks {
		if b.Label == label {
			found = append(found, b)
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("transaction marker %s occurs %d times: fail-closed", txID, len(found))
	}
	raw, err := store.ExtractBlock(doc, label)
	if err != nil {
		return nil, err
	}
	var marker transactionMarker
	if err := json.Unmarshal(raw, &marker); err != nil {
		return nil, fmt.Errorf("transaction marker %s corrupt: fail-closed: %w", txID, err)
	}
	if marker.TransactionID != txID || !hex64.MatchString(marker.PayloadDigest) {
		return nil, fmt.Errorf("transaction marker %s identity/digest invalid: fail-closed", txID)
	}
	return &marker, nil
}

func validateJournalSection(j *dispatchJournal, section []byte) (*State, error) {
	markerStart := []byte("- acrelay_transaction " + j.TransactionID + ": encoding=")
	i := strings.LastIndex(string(section), string(markerStart))
	if i < 0 || store.Digest(section[:i]) != j.PayloadDigest {
		return nil, fmt.Errorf("dispatch journal %s payload digest mismatch: fail-closed", j.TransactionID)
	}
	marker, err := findTransactionMarker(string(section), j.TransactionID)
	if err != nil {
		return nil, err
	}
	if marker == nil || marker.Kind != j.Kind || marker.PayloadDigest != j.PayloadDigest {
		return nil, fmt.Errorf("dispatch journal %s marker mismatch: fail-closed", j.TransactionID)
	}
	raw, err := store.ExtractBlock(string(section), fmt.Sprintf("%s%d", statePrefix, j.ExpectedStateSeq))
	if err != nil {
		return nil, fmt.Errorf("dispatch journal %s embedded state: %w", j.TransactionID, err)
	}
	var embedded State
	if err := json.Unmarshal(raw, &embedded); err != nil {
		return nil, fmt.Errorf("dispatch journal %s embedded state corrupt: %w", j.TransactionID, err)
	}
	if embedded.Seq != j.ExpectedStateSeq || embedded.CollaborationID != j.CollaborationID ||
		embedded.ObjectiveID != j.ObjectiveID || embedded.TargetRevision != j.TargetRevision {
		return nil, fmt.Errorf("dispatch journal %s embedded state lineage mismatch: fail-closed", j.TransactionID)
	}
	found := false
	for _, tx := range embedded.Transactions {
		if tx.ID == j.TransactionID && tx.Kind == j.Kind {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("dispatch journal %s embedded state lacks transaction ledger entry: fail-closed", j.TransactionID)
	}
	return &embedded, nil
}

func unknownJournalPayload(st *State, j *dispatchJournal) (string, error) {
	st.Transactions = append(st.Transactions, TransactionState{
		ID: j.TransactionID, Kind: j.Kind, RoundIndex: j.RoundIndex,
		AttemptIndex: j.AttemptIndex, Reviewer: j.Reviewer, Result: "unknown",
		Execution: string(kernel.ExecUnknown),
		CauseCode: adapter.CauseJournalReconciledUnknown, CauseSource: adapter.CauseSourceObserved,
	})
	var header string
	switch j.Kind {
	case "review":
		if j.RoundIndex != len(st.Rounds) || j.AttemptIndex != 0 {
			return "", fmt.Errorf("prepared review journal round/attempt does not follow canonical state: fail-closed")
		}
		st.Rounds = append(st.Rounds, RoundState{
			Index: j.RoundIndex, Attempts: []string{string(kernel.ExecUnknown)},
			Outcome: string(review.OutcomeFailed), Revision: st.TargetRevision,
			TransactionID: j.TransactionID,
		})
		st.Governance = string(kernel.GovDecisionRequired)
		header = fmt.Sprintf("\n## round R%d attempt A%d\n- transaction_id: %s\n- outcome: failed\n- execution: UNKNOWN\n- dispatch_error: %q\n",
			j.RoundIndex, j.AttemptIndex, j.TransactionID,
			"host process ended after durable intent; reviewer execution cannot be determined; automatic retry forbidden")
	case "confirmation":
		header = fmt.Sprintf("\n## confirmation dispatch UNKNOWN R%d\n- transaction_id: %s\n- execution: UNKNOWN\n- dispatch_error: %q\n",
			j.RoundIndex, j.TransactionID,
			"host process ended after durable intent; reviewer execution cannot be determined; automatic retry forbidden")
	default:
		return "", fmt.Errorf("unknown journal kind %q", j.Kind)
	}
	block, err := stateSection(st)
	if err != nil {
		return "", err
	}
	return header + block, nil
}

func reconcileDispatchJournal(canonical, journalPath string) (*State, error) {
	var out *State
	err := withCanonicalLock(canonical, func() error {
		j, _, err := loadDispatchJournal(canonical, journalPath)
		if err != nil {
			return err
		}
		cur, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if cur == nil {
			return fmt.Errorf("reconcile refused: canonical has no state")
		}
		doc, err := store.ReadAll(canonical)
		if err != nil {
			return err
		}
		if marker, err := findTransactionMarker(doc, j.TransactionID); err != nil {
			return err
		} else if marker != nil {
			if marker.Kind != j.Kind || marker.PayloadDigest != j.PayloadDigest {
				return fmt.Errorf("transaction %s already exists with different evidence: fail-closed", j.TransactionID)
			}
			if err := removeDispatchJournal(journalPath); err != nil {
				return err
			}
			out = cur
			return nil
		}
		if cur.CollaborationID != j.CollaborationID || cur.ObjectiveID != j.ObjectiveID ||
			cur.Seq != j.StateSeq || cur.Seq+1 != j.ExpectedStateSeq || cur.TargetRevision != j.TargetRevision {
			return fmt.Errorf("reconcile refused: dispatch journal lineage does not match canonical: fail-closed")
		}
		if !hex64.MatchString(j.PreSnapshot) {
			return fmt.Errorf("reconcile refused: dispatch pre-snapshot malformed: fail-closed")
		}
		if j.Phase == journalPhasePrepared {
			payload, err := unknownJournalPayload(cur, j)
			if err != nil {
				return err
			}
			if _, err := setJournalSection(journalPath, j, journalPhaseUnknown, payload); err != nil {
				return err
			}
		}
		section, err := decodeJournalSection(j)
		if err != nil {
			return err
		}
		if _, err := validateJournalSection(j, section); err != nil {
			return err
		}
		rev, err := store.Revision(canonical)
		if err != nil {
			return err
		}
		note := fmt.Sprintf("\n## dispatch reconcile\n- transaction_id: %s\n- journal: %s\n- pre_snapshot: %s\n- current_revision: %s\n- diverged_since_dispatch: %v\n",
			j.TransactionID, filepath.Base(journalPath), j.PreSnapshot, rev, rev != j.PreSnapshot)
		if _, err := store.AppendAtomic(canonical, note+string(section), rev); err != nil {
			return err
		}
		if err := removeDispatchJournal(journalPath); err != nil {
			return fmt.Errorf("dispatch reconcile applied but journal cleanup failed: %w", err)
		}
		out, err = LoadState(canonical)
		return err
	})
	return out, err
}

func transactionIDFromPath(canonical, path string) (string, error) {
	prefix := canonical + ".dispatch-"
	if filepath.Dir(path) != filepath.Dir(canonical) || !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, ".json") {
		return "", fmt.Errorf("journal %s is not bound to canonical %s", path, canonical)
	}
	txID := strings.TrimSuffix(strings.TrimPrefix(path, prefix), ".json")
	if !transactionIDPattern.MatchString(txID) {
		return "", fmt.Errorf("journal filename has invalid transaction identity: fail-closed")
	}
	return txID, nil
}

// AbandonTransaction is the declared owner/arbiter escape for a corrupt or
// otherwise un-reconcilable dispatch journal. It records the anomaly first;
// only then is the private journal moved out of the pending guard namespace.
func AbandonTransaction(canonical, journalPath, actor, role, reason string) (*State, string, error) {
	if strings.TrimSpace(actor) == "" || (role != "owner" && role != "arbiter") || strings.TrimSpace(reason) == "" {
		return nil, "", fmt.Errorf("abandon-transaction requires declared actor, role owner|arbiter, and nonblank reason")
	}
	txID, err := transactionIDFromPath(canonical, journalPath)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(journalPath)
	if err != nil {
		return nil, "", err
	}
	digest := store.Digest(raw)
	kind, roundIndex, attemptIndex, reviewer := "unknown", -1, -1, ""
	var parsed dispatchJournal
	if json.Unmarshal(raw, &parsed) == nil && parsed.TransactionID == txID {
		if parsed.Kind == "review" || parsed.Kind == "confirmation" {
			kind = parsed.Kind
		}
		roundIndex, attemptIndex, reviewer = parsed.RoundIndex, parsed.AttemptIndex, parsed.Reviewer
	}
	var out *State
	var quarantine string
	err = withCanonicalLock(canonical, func() error {
		st, err := LoadState(canonical)
		if err != nil {
			return err
		}
		if st == nil {
			return fmt.Errorf("abandon refused: canonical has no state")
		}
		already := false
		for _, tx := range st.Transactions {
			if tx.ID == txID {
				if tx.Result != "abandoned" || tx.JournalDigest != digest {
					return fmt.Errorf("transaction %s already has a conflicting canonical disposition: fail-closed", txID)
				}
				already = true
			}
		}
		if !already {
			rev, err := store.Revision(canonical)
			if err != nil {
				return err
			}
			st.Transactions = append(st.Transactions, TransactionState{
				ID: txID, Kind: kind, RoundIndex: roundIndex, AttemptIndex: attemptIndex,
				Reviewer: reviewer, Result: "abandoned", JournalDigest: digest,
			})
			header := fmt.Sprintf("\n## dispatch transaction abandoned\n- transaction_id: %s\n- journal_digest: %s\n- actor: %s\n- role: %s\n- reason: %s\n- anomaly: un-reconcilable private dispatch journal quarantined; execution remains unaudited\n",
				txID, digest, actor, role, reason)
			if err := appendState(canonical, st, header, rev); err != nil {
				return err // canonical-first: never quarantine after a failed record
			}
		}
		quarantine = canonical + ".quarantine-" + txID + "-" + digest[:12] + ".json"
		if err := os.Chmod(journalPath, 0o600); err != nil {
			return err
		}
		if err := store.RenamePrivate(journalPath, quarantine); err != nil {
			return err
		}
		out, err = LoadState(canonical)
		return err
	})
	return out, quarantine, err
}
