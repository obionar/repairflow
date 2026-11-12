package app_test

import (
	"context"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

func TestUserPasswordLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.CreateUser(ctx, "setup", "admin", "password12345", app.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, "setup", "tech", "password12345", app.RoleTechnician); err != nil {
		t.Fatal(err)
	}

	_, token, err := svc.Login(ctx, "admin", "password12345")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LookupSession(ctx, token); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, "admin", app.RoleAdmin, "admin", "new-password-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LookupSession(ctx, token); err == nil {
		t.Fatal("password reset must revoke all sessions")
	}
	if _, _, err := svc.Login(ctx, "admin", "password12345"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("old password must fail: %v", err)
	}
	if _, _, err := svc.Login(ctx, "admin", "new-password-1"); err != nil {
		t.Fatalf("new password must work: %v", err)
	}
}

func TestUserActiveLifecycleAndLastAdminProtection(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.CreateUser(ctx, "setup", "admin", "password12345", app.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, "setup", "other", "password12345", app.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateUser(ctx, "setup", "third", "password12345", app.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	_, token, err := svc.Login(ctx, "other", "password12345")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := svc.SetUserActive(ctx, "admin", app.RoleAdmin, "admin", false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "admin", "password12345"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("disabled user must not log in: %v", err)
	}
	if _, err := svc.SetUserActive(ctx, "admin", app.RoleAdmin, "other", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.LookupSession(ctx, token); err == nil {
		t.Fatal("disabling a user must revoke their sessions")
	}
	if _, err := svc.SetUserActive(ctx, "admin", app.RoleAdmin, "other", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "other", "password12345"); err != nil {
		t.Fatalf("re-enabled user must log in: %v", err)
	}

	// Disable the second admin, then refuse disabling the last active admin.
	if _, err := svc.SetUserActive(ctx, "admin", app.RoleAdmin, "other", false); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetUserActive(ctx, "admin", app.RoleAdmin, "admin", false); errs.CodeOf(err) != errs.CodeConflict {
		t.Fatalf("last active admin disable must be refused: %v", err)
	}
}

func TestPasswordMinimumLength(t *testing.T) {
	if app.MinPasswordLength != 6 {
		t.Fatalf("policy length = %d", app.MinPasswordLength)
	}
	if _, err := app.HashPassword("five5"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("five characters must be refused: %v", err)
	}
	if _, err := app.HashPassword("sixsix"); err != nil {
		t.Fatalf("six characters must be accepted: %v", err)
	}
}

func TestCheckPasswordRejectsMalformedHashes(t *testing.T) {
	valid, err := app.HashPassword("password12345")
	if err != nil {
		t.Fatal(err)
	}
	if !app.CheckPassword(valid, "password12345") {
		t.Fatal("valid password hash must verify")
	}
	for _, malformed := range []string{
		"", "$argon2id$v=19$$$", "$argon2id$v=19$AA$AA",
		"$argon2id$v=19$QUJDREVGRw$AA", "$argon2id$v=19$QUJDREVGRw$",
	} {
		if app.CheckPassword(malformed, "password12345") {
			t.Errorf("malformed hash %q must not authenticate", malformed)
		}
	}
}

func TestUserDatabaseFailuresAreInternal(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.CreateUser(ctx, "setup", "admin", "password12345", app.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := svc.DB().Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.Login(ctx, "admin", "password12345"); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("database failure must not be misreported as bad credentials: %v", err)
	}
	if err := svc.ResetPassword(ctx, "admin", app.RoleAdmin, "admin", "new-password-1"); errs.CodeOf(err) != errs.CodeInternal {
		t.Fatalf("database failure must remain internal: %v", err)
	}
}

func TestUserPasswordResetRequiresAdmin(t *testing.T) {
	ctx := context.Background()
	svc := newService(t)
	if _, err := svc.CreateUser(ctx, "setup", "admin", "password12345", app.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := svc.ResetPassword(ctx, "web:tech", app.RoleTechnician, "admin", "new-password-1"); errs.CodeOf(err) != errs.CodePermission {
		t.Fatalf("non-admin reset must be refused: %v", err)
	}
	if err := svc.ResetPassword(ctx, "web:admin", app.RoleAdmin, "missing", "new-password-1"); errs.CodeOf(err) != errs.CodeNotFound {
		t.Fatalf("unknown user reset: %v", err)
	}
	if err := svc.ResetPassword(ctx, "web:admin", app.RoleAdmin, "admin", "short"); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("short password reset: %v", err)
	}
}
