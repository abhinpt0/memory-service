package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// --- Defect 12 / Bug 6: classifyMigrationError must use typed sentinels, not string matching ---

func TestClassifyMigrationErrorReturnsStableCode(t *testing.T) {
	t.Parallel()
	// Bug 6 fix: classification is now based on typed sentinel types, not error message text.
	// Opaque raw errors (even with incidentally matching words) always return transient_error.
	cases := []struct {
		err      error
		wantCode string
	}{
		{nil, ""},
		{context.DeadlineExceeded, "deadline_exceeded"},
		{context.Canceled, "canceled"},
		// Typed sentinels produce deterministic stable codes.
		{&migErrCASConflict{cause: errors.New("inner")}, "cas_conflict"},
		{&migErrRescheduleFailed{cause: errors.New("inner")}, "reschedule_failed"},
		{&migErrScanFailed{cause: errors.New("inner")}, "scan_failed"},
		{&migErrLoadFailed{cause: errors.New("inner")}, "load_failed"},
		{&migErrStateUpdateFailed{cause: errors.New("inner")}, "state_update_failed"},
		// Wrapped typed sentinels still produce stable codes.
		{fmt.Errorf("wrap: %w", &migErrCASConflict{cause: errors.New("inner")}), "cas_conflict"},
		// Plain text errors (even incidentally matching old string patterns) → transient_error.
		{errors.New("something CAS conflict"), "transient_error"},
		{errors.New("revision conflict on row"), "transient_error"},
		{errors.New("reschedule failed"), "transient_error"},
		{errors.New("find memories error"), "transient_error"},
		{errors.New("load migration record"), "transient_error"},
		{errors.New("persist state failed"), "transient_error"},
		{errors.New("some_unknown_transient_error"), "transient_error"},
	}
	for _, c := range cases {
		got := classifyMigrationError(c.err)
		if got != c.wantCode {
			t.Errorf("classifyMigrationError(%v) = %q, want %q", c.err, got, c.wantCode)
		}
	}
}

func TestClassifyMigrationErrorNeverReturnsRawText(t *testing.T) {
	t.Parallel()
	rawErrors := []error{
		errors.New("pgconn: FATAL: too many connections for role"),
		errors.New("dial tcp 10.0.0.1:5432: connection refused"),
		errors.New("mongo: HostUnreachable: mongodb+srv://user:password@cluster"),
		errors.New("rpc error: code = Unavailable desc = connection reset by peer"),
		errors.New("SELECT id, key FROM memories WHERE namespace=$1 LIMIT 50"),
	}
	for _, err := range rawErrors {
		code := classifyMigrationError(err)
		// The stable code must not contain the raw error message.
		if code == err.Error() {
			t.Errorf("classifyMigrationError returned raw error text: %q", code)
		}
		if len(code) > 64 {
			t.Errorf("classifyMigrationError code too long (%d chars): %q", len(code), code)
		}
	}
}
