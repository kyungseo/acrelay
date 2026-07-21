package relay

import (
	"context"
	"strings"
	"testing"

	"github.com/kyungseo/acrelay/internal/adapter"
)

// FEAT-20260721-002: an ambiguous termination (signal kill, parent cancel)
// is UNKNOWN — never a FAILED assertion that execution did not complete —
// and the typed cause lands on the transaction ledger.
func TestAmbiguousTerminationIsUnknownWithTypedCause(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{{
		Ambiguous: true, CauseCode: adapter.CauseTerminatedSignal, CauseSource: adapter.CauseSourceObserved,
	}})
	st, outcome, err := s.Review(context.Background(), "review", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" || len(st.Rounds) != 1 ||
		st.Rounds[0].Attempts[len(st.Rounds[0].Attempts)-1] != "UNKNOWN" {
		t.Fatalf("signal termination must be UNKNOWN: outcome=%s rounds=%+v", outcome, st.Rounds)
	}
	tx := st.Transactions[len(st.Transactions)-1]
	if tx.Execution != "UNKNOWN" || tx.CauseCode != adapter.CauseTerminatedSignal ||
		tx.CauseSource != adapter.CauseSourceObserved {
		t.Fatalf("transaction ledger must carry the typed cause: %+v", tx)
	}
	// UNKNOWN keeps the cross-round no-retry guard.
	if _, _, err := s.Review(context.Background(), "again", adapter.Request{}); err == nil ||
		!strings.Contains(err.Error(), "UNKNOWN") {
		t.Fatalf("UNKNOWN round must forbid re-dispatch: %v", err)
	}
	// AR-2 Option B: the private status surface shows the allowlisted phrase.
	out, err := Status(s.Canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "last failure cause: "+adapter.CauseTerminatedSignal) ||
		!strings.Contains(out, adapter.CausePhrase(adapter.CauseTerminatedSignal)) {
		t.Fatalf("status must render the allowlisted cause phrase:\n%s", out)
	}
}

// A FAILED dispatch records its cause without changing FAILED semantics.
func TestFailedDispatchRecordsBoundedCause(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{{
		Err:       errFake("vendor turn failed"),
		CauseCode: adapter.CauseVendorTurnFailed, CauseSource: adapter.CauseSourceVendorDeclared,
	}})
	st, outcome, err := s.Review(context.Background(), "review", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" || st.Rounds[0].Attempts[len(st.Rounds[0].Attempts)-1] != "FAILED" {
		t.Fatalf("vendor-declared failure must stay FAILED: %+v", st.Rounds)
	}
	tx := st.Transactions[len(st.Transactions)-1]
	if tx.Execution != "FAILED" || tx.CauseCode != adapter.CauseVendorTurnFailed {
		t.Fatalf("FAILED cause not persisted: %+v", tx)
	}
}

// Redaction: the persisted cause fields carry only registry values — vendor
// text (which may contain secrets/paths) stays in the raw canonical blocks,
// never in the typed ledger. Tampered cause codes fail the load gate.
func TestCauseLedgerIsBoundedAndTamperFailsClosed(t *testing.T) {
	secret := "sk-SECRET-TOKEN-/private/leak/path"
	s, _, _ := newSession(t, []adapter.FakeResult{{
		Err:       errFake("quota exceeded: " + secret),
		CauseCode: adapter.CauseUnknown, CauseSource: adapter.CauseSourceObserved,
	}})
	st, _, err := s.Review(context.Background(), "review", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	tx := st.Transactions[len(st.Transactions)-1]
	if strings.Contains(tx.CauseCode, secret) || strings.Contains(tx.CauseSource, secret) {
		t.Fatal("cause fields must never carry vendor text")
	}
	if err := adapter.ValidCause(adapter.FailureCause{Code: "made-up.cause", Source: "observed"}); err == nil {
		t.Fatal("non-registry cause code must be invalid")
	}
	if err := adapter.ValidCause(adapter.FailureCause{Code: adapter.CauseUnknown, Source: "invented-source"}); err == nil {
		t.Fatal("non-registry cause source must be invalid")
	}
	// Tamper: rewrite the persisted cause code and expect a load failure.
	tampered := &State{}
	*tampered = *st
	tampered.Transactions = append([]TransactionState(nil), st.Transactions...)
	tampered.Transactions[len(tampered.Transactions)-1].CauseCode = "vendor.fabricated"
	if err := validateState(tampered, tampered.Seq); err == nil ||
		!strings.Contains(err.Error(), "not in the failure-cause v0.1 registry") {
		t.Fatalf("tampered cause code must fail closed: %v", err)
	}
	succeeded := &State{}
	*succeeded = *st
	succeeded.Transactions = append([]TransactionState(nil), st.Transactions...)
	succeeded.Transactions[len(succeeded.Transactions)-1].Execution = "SUCCEEDED"
	if err := validateState(succeeded, succeeded.Seq); err == nil ||
		!strings.Contains(err.Error(), "failure cause on a succeeded execution") {
		t.Fatalf("cause on succeeded execution must fail closed: %v", err)
	}
}

type errFake string

func (e errFake) Error() string { return string(e) }

// R1-CX-F6 / R0-CX-F2 end-to-end: a vendor-side resume-not-found after child
// start is a consuming FAILED review attempt with the typed cause on the
// ledger — verified through the relay, not just the adapter.
func TestResumeInvalidConsumesReviewAttemptWithCause(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{
		approve(), // R0 establishes a session to resume
		{Err: errFake("No conversation found with session ID"),
			CauseCode: adapter.CauseResumeHandleInvalid, CauseSource: adapter.CauseSourceInferred},
	})
	if _, _, err := s.Review(context.Background(), "r0", adapter.Request{}); err != nil {
		t.Fatal(err)
	}
	// R1 resumes the stored session (default round bound 3 allows a second
	// formal round); the fake returns the vendor resume-not-found failure.
	st, outcome, err := s.Review(context.Background(), "r1", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "failed" {
		t.Fatalf("resume-invalid must be a FAILED outcome: %s", outcome)
	}
	last := st.Rounds[len(st.Rounds)-1]
	if last.Attempts[len(last.Attempts)-1] != "FAILED" {
		t.Fatalf("resume-invalid must consume the attempt as FAILED: %+v", last)
	}
	tx := st.Transactions[len(st.Transactions)-1]
	if tx.Execution != "FAILED" || tx.CauseCode != adapter.CauseResumeHandleInvalid ||
		tx.CauseSource != adapter.CauseSourceInferred {
		t.Fatalf("resume-invalid cause must persist (inferred): %+v", tx)
	}
}

// R1-CX-F2/F3 tamper matrix on the persisted transaction ledger.
func TestTransactionLedgerTamperFailsClosed(t *testing.T) {
	s, _, _ := newSession(t, []adapter.FakeResult{{
		Err: errFake("boom"), CauseCode: adapter.CauseVendorTurnFailed, CauseSource: adapter.CauseSourceVendorDeclared,
	}})
	st, _, err := s.Review(context.Background(), "review", adapter.Request{})
	if err != nil {
		t.Fatal(err)
	}
	base := func() *State {
		clone := *st
		clone.Transactions = append([]TransactionState(nil), st.Transactions...)
		return &clone
	}
	last := func(x *State) *TransactionState { return &x.Transactions[len(x.Transactions)-1] }

	for _, tc := range []struct {
		name string
		mut  func(*State)
		want string
	}{
		{"cause removed on FAILED", func(x *State) { last(x).CauseCode, last(x).CauseSource = "", "" }, "mandatory typed cause"},
		{"execution removed", func(x *State) { last(x).Execution = "" }, "lacks a typed execution"},
		{"source upgraded to observed", func(x *State) { last(x).CauseSource = "observed" }, "does not permit source"},
		{"result/execution contradiction", func(x *State) { last(x).Result = "unknown" }, "contradicts execution"},
		{"fabricated code", func(x *State) { last(x).CauseCode = "vendor.fabricated" }, "not in the failure-cause v0.1 registry"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := base()
			tc.mut(x)
			if err := validateState(x, x.Seq); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("tamper %q must fail closed with %q: %v", tc.name, tc.want, err)
			}
		})
	}
}
