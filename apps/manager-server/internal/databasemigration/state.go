package databasemigration

import (
	"errors"
	"fmt"
	"time"
)

func CanAdvancePhase(from, to MigrationPhase) bool {
	order := map[MigrationPhase]MigrationPhase{
		PhaseEnableDualWrite: PhaseCopyHistory,
		PhaseCopyHistory:     PhaseRebuildDerived,
		PhaseRebuildDerived:  PhaseValidate,
		PhaseValidate:        PhaseReadyToCutover,
		PhaseReadyToCutover:  PhaseCompleted,
	}
	return order[from] == to
}

func ValidateMigrationTransition(current Migration, nextPhase MigrationPhase, nextStatus RunStatus) error {
	if current.Status == StatusCanceled || current.Status == StatusSucceeded {
		return fmt.Errorf("%w: terminal status %q", ErrInvalidTransition, current.Status)
	}
	if nextPhase != current.Phase && !CanAdvancePhase(current.Phase, nextPhase) {
		return fmt.Errorf("%w: phase %q to %q", ErrInvalidTransition, current.Phase, nextPhase)
	}
	switch nextStatus {
	case StatusRunning:
		if current.Status != StatusRunning && current.Status != StatusPaused && current.Status != StatusFailed {
			return fmt.Errorf("%w: status %q to running", ErrInvalidTransition, current.Status)
		}
	case StatusPaused:
		if current.Status != StatusRunning {
			return fmt.Errorf("%w: only a running migration can be paused", ErrInvalidTransition)
		}
	case StatusCanceled:
		if current.Status != StatusRunning && current.Status != StatusPaused && current.Status != StatusFailed {
			return fmt.Errorf("%w: status %q to canceled", ErrInvalidTransition, current.Status)
		}
	case StatusFailed:
		if current.Status != StatusRunning {
			return fmt.Errorf("%w: only a running migration can fail", ErrInvalidTransition)
		}
	case StatusSucceeded:
		if current.Phase != PhaseReadyToCutover || nextPhase != PhaseCompleted || current.Status != StatusRunning {
			return fmt.Errorf("%w: migration is not ready to cut over", ErrInvalidTransition)
		}
	default:
		return fmt.Errorf("%w: unknown status %q", ErrInvalidTransition, nextStatus)
	}
	if nextPhase == PhaseReadyToCutover && nextStatus == StatusRunning &&
		(current.Validation == nil || !current.Validation.Passed || current.ValidationToken == "") {
		return fmt.Errorf("%w: successful validation is required before cutover", ErrInvalidTransition)
	}
	return nil
}

func ReplicationHealth(state ReplicationState, now time.Time) ReplicationState {
	state.Warning = false
	state.Stalled = false
	if state.BacklogRows <= 0 {
		return state
	}
	lastProgress := state.LastProgressAtMS
	if lastProgress == 0 {
		lastProgress = state.HeartbeatAtMS
	}
	if lastProgress == 0 {
		return state
	}
	age := now.Sub(time.UnixMilli(lastProgress))
	state.Warning = age >= ReplicationWarnAfter
	state.Stalled = age >= ReplicationStallAfter
	return state
}

func EvaluateCleanupSafety(safety CleanupSafety, now time.Time) error {
	reasons := make([]error, 0, 5)
	if !safety.MySQLAvailable {
		reasons = append(reasons, errors.New("mysql is unavailable"))
	}
	if !safety.MigrationComplete {
		reasons = append(reasons, errors.New("history migration is incomplete"))
	}
	if !safety.ReplicationCaughtUp {
		reasons = append(reasons, errors.New("replication has pending changes"))
	}
	if !safety.ValidationPassed || safety.ValidationToken == "" {
		reasons = append(reasons, errors.New("a successful validation token is required"))
	}
	if safety.ValidationPassed && safety.ValidationMaxAge > 0 {
		if safety.ValidationAtMS <= 0 || now.Sub(time.UnixMilli(safety.ValidationAtMS)) > safety.ValidationMaxAge {
			reasons = append(reasons, errors.New("validation has expired"))
		}
	}
	if len(reasons) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %v", ErrCleanupUnsafe, errors.Join(reasons...))
}
