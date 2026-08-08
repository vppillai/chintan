package service

import (
	"context"
	"errors"
	"testing"

	"github.com/vppillai/chintan/backend/internal/model"
	"github.com/vppillai/chintan/backend/internal/repository"
	"github.com/vppillai/chintan/backend/internal/repository/memory"
)

func TestGetSettingsReturnsDefaultsForAUserWhoHasNeverSavedAny(t *testing.T) {
	svc := NewSettingsService(bareNotFoundStore{memory.NewStore()})

	got, err := svc.GetSettings(context.Background(), "user1")
	if err != nil {
		t.Fatalf("GetSettings: %v, want defaults and no error for a tenant with no row", err)
	}
	if got != repository.DefaultSettings() {
		t.Fatalf("settings = %+v, want the defaults %+v", got, repository.DefaultSettings())
	}
}

// The comparison is errors.Is, not identity. One fmt.Errorf("...: %w") added
// anywhere below this turns "no settings yet" — the state every new user is in
// — into a 500 on their first request.
func TestGetSettingsTreatsAWrappedNotFoundAsDefaults(t *testing.T) {
	svc := NewSettingsService(wrappedNotFoundStore{memory.NewStore()})

	got, err := svc.GetSettings(context.Background(), "user1")
	if err != nil {
		t.Fatalf("GetSettings: %v, want defaults: the store's error wraps repository.ErrNotFound", err)
	}
	if got != repository.DefaultSettings() {
		t.Fatalf("settings = %+v, want the defaults %+v", got, repository.DefaultSettings())
	}
}

// A real store failure is not a new user. Substituting defaults for it would
// serve a tenant somebody else's cleanup mode and retention during an outage.
func TestGetSettingsPropagatesARealStoreFailure(t *testing.T) {
	boom := errors.New("dynamodb: ProvisionedThroughputExceededException")
	svc := NewSettingsService(errSettingsStore{Store: memory.NewStore(), err: boom})

	got, err := svc.GetSettings(context.Background(), "user1")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the store's failure to reach the caller", err)
	}
	if got != (model.Settings{}) {
		t.Fatalf("settings = %+v, want the zero value alongside an error, not a plausible-looking record", got)
	}
}

func TestUpdateSettingsRoundTripsThroughTheStore(t *testing.T) {
	store := memory.NewStore()
	svc := NewSettingsService(store)
	ctx := context.Background()

	want := model.Settings{
		CleanupMode:         model.CleanupPolished,
		RetentionDays:       30,
		Theme:               model.ThemeNocturne,
		DailySpendCapMicros: 250000,
	}
	if err := svc.UpdateSettings(ctx, "user1", want); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	got, err := svc.GetSettings(ctx, "user1")
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got != want {
		t.Fatalf("settings = %+v, want %+v", got, want)
	}
}

// ValidateSettings returns the record that will be stored, and the caller must
// use that value. v1 echoed the request body back, which hid every coercion: a
// theme of "" stored as ink came back as "" and the client kept sending it.
func TestValidateSettingsReturnsWhatWillBeStoredNotWhatWasSent(t *testing.T) {
	sent := model.Settings{RetentionDays: 30}

	got, err := ValidateSettings(sent)
	if err != nil {
		t.Fatalf("ValidateSettings: %v", err)
	}
	if got == sent {
		t.Fatal("ValidateSettings returned the request unchanged; the empty cleanup mode and theme were coerced on the way to the store and the client is not being told")
	}
	if got.CleanupMode != model.CleanupFaithful {
		t.Errorf("cleanup mode = %q, want %q for an empty request field", got.CleanupMode, model.CleanupFaithful)
	}
	if got.Theme != model.ThemeInk {
		t.Errorf("theme = %q, want %q for an empty request field", got.Theme, model.ThemeInk)
	}
	if got.RetentionDays != 30 {
		t.Errorf("retention days = %d, want the requested 30 carried through", got.RetentionDays)
	}
}

func TestValidateSettingsKeepsAValueTheCallerActuallyChose(t *testing.T) {
	sent := model.Settings{
		CleanupMode:         model.CleanupPolished,
		RetentionDays:       model.MaxRetentionDays,
		Theme:               model.ThemeSystem,
		DailySpendCapMicros: 1,
	}

	got, err := ValidateSettings(sent)
	if err != nil {
		t.Fatalf("ValidateSettings(%+v): %v, want the boundary value accepted", sent, err)
	}
	if got != sent {
		t.Fatalf("settings = %+v, want the explicit request %+v preserved", got, sent)
	}
}

func TestValidateSettingsRejectsARetentionThatIsNotAPolicy(t *testing.T) {
	for _, days := range []int{-1, model.MaxRetentionDays + 1} {
		got, err := ValidateSettings(model.Settings{RetentionDays: days})
		if !errors.Is(err, ErrInvalidRetentionDays) {
			t.Errorf("ValidateSettings(retention=%d) err = %v, want ErrInvalidRetentionDays", days, err)
		}
		if got != (model.Settings{}) {
			t.Errorf("ValidateSettings(retention=%d) = %+v, want the zero value: a caller that ignores the error must not receive a half-coerced record", days, got)
		}
	}
}

func TestValidateSettingsRejectsValuesOutsideTheDeclaredSets(t *testing.T) {
	cases := []struct {
		name string
		in   model.Settings
		want error
	}{
		{"unknown cleanup mode", model.Settings{CleanupMode: model.CleanupMode("tidy")}, ErrInvalidCleanupMode},
		{"unknown theme", model.Settings{Theme: model.Theme("purple")}, ErrInvalidTheme},
		{"negative spend cap", model.Settings{DailySpendCapMicros: -1}, ErrInvalidSpendCap},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ValidateSettings(tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidateSettings(%+v) err = %v, want %v", tc.in, err, tc.want)
			}
			if got != (model.Settings{}) {
				t.Fatalf("ValidateSettings(%+v) = %+v, want the zero value", tc.in, got)
			}
		})
	}
}

// A record written before v2 carries no theme and no cleanup mode. A GET must
// still answer the shape the contract declares rather than empty strings the
// client has no case for.
func TestNormalizeSettingsFillsTheFieldsAPreV2RecordDoesNotCarry(t *testing.T) {
	got := NormalizeSettings(model.Settings{RetentionDays: -5})

	if got.CleanupMode != model.CleanupFaithful {
		t.Errorf("cleanup mode = %q, want %q", got.CleanupMode, model.CleanupFaithful)
	}
	if got.Theme != model.ThemeInk {
		t.Errorf("theme = %q, want %q", got.Theme, model.ThemeInk)
	}
	if got.RetentionDays != 0 {
		t.Errorf("retention days = %d, want a negative stored value floored at 0", got.RetentionDays)
	}
}

func TestNormalizeSettingsLeavesACompleteRecordAlone(t *testing.T) {
	want := model.Settings{
		CleanupMode:   model.CleanupPolished,
		RetentionDays: 7,
		Theme:         model.ThemeNocturne,
	}
	if got := NormalizeSettings(want); got != want {
		t.Fatalf("NormalizeSettings(%+v) = %+v, want it unchanged", want, got)
	}
}
