package app_test

import (
	"context"
	"encoding/base64"
	"sync"
	"testing"

	"golang.org/x/crypto/argon2"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// TestCheckPasswordHonoursStoredParameters: a PHC hash carries its own Argon2
// settings. Ignoring them means every account locks out the day the constants
// are tuned (or a hash from another parameter set is imported).
func TestCheckPasswordHonoursStoredParameters(t *testing.T) {
	password := "secret1"
	salt := []byte("0123456789abcdef")
	digest := argon2.IDKey([]byte(password), salt, 2, 8192, 1, 32)
	stored := "$argon2id$v=19,m=8192,t=2,p=1$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(digest)
	if !app.CheckPassword(stored, password) {
		t.Fatal("a hash with its own m/t/p parameters must still verify")
	}
	if app.CheckPassword(stored, "wrong-password") {
		t.Fatal("wrong password accepted")
	}
	if app.CheckPassword(stored, "") {
		t.Fatal("empty password accepted")
	}
	// A hash whose parameters demand impossible work is refused, not executed.
	bad := "$argon2id$v=19,m=999999999999,t=2,p=1$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(digest)
	if app.CheckPassword(bad, password) {
		t.Fatal("out-of-range parameters must not verify")
	}
}

// TestConcurrentLastAdminDisable: two operators disabling each other at once
// must not leave the workspace with zero active administrators.
func TestConcurrentLastAdminDisable(t *testing.T) {
	ctx := context.Background()
	a, b := twoServices(t)
	for _, name := range []string{"admin-one", "admin-two"} {
		if _, err := a.CreateUser(ctx, "setup", name, "password12345", app.RoleAdmin); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i, svc := range []*app.Service{a, b} {
		name := "admin-one"
		if i == 1 {
			name = "admin-two"
		}
		wg.Add(1)
		go func(svc *app.Service, name string) {
			defer wg.Done()
			_, err := svc.SetUserActive(ctx, "tester", app.RoleAdmin, name, false)
			results <- err
		}(svc, name)
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errs.CodeOf(err) != errs.CodeConflict {
			t.Fatalf("unexpected failure: %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("exactly one disable may succeed, got %d", succeeded)
	}
	users, err := a.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activeAdmins := 0
	for _, u := range users {
		if u.Role == app.RoleAdmin && u.Active {
			activeAdmins++
		}
	}
	if activeAdmins != 1 {
		t.Fatalf("active administrators after the race: %d", activeAdmins)
	}
}
