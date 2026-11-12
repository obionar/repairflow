package seed

import (
	"context"
	"strings"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

// DemoUserRole is the role of the optional demo operator account. It is admin
// so a walkthrough can also exercise settings, role handling and admin-only
// actions (purge, user administration).
const DemoUserRole = "admin"

// UserResult reports the optional demo operator account. The password is
// never part of the result (or of any log/output line).
type UserResult struct {
	Username       string `json:"username"`
	Role           string `json:"role"`
	Created        bool   `json:"created"`
	AlreadyExisted bool   `json:"already_existed"`
}

// ensureDemoUser provisions one demo operator account when the caller asked
// for it. Rules:
//
//   - no account is created unless a username is requested (seeding data alone
//     never creates credentials);
//   - the password must be supplied out-of-band (environment, never argv);
//   - an existing account with that username is never modified — its password
//     is left alone and the caller is told it already existed.
func ensureDemoUser(ctx context.Context, svc *app.Service, opts Options) (*UserResult, error) {
	username := strings.TrimSpace(opts.User)
	if username == "" {
		if opts.Password != "" {
			return nil, errs.New(errs.CodeUsage,
				"a demo password was supplied without a username; pass --demo-user USERNAME")
		}
		return nil, nil
	}
	if opts.Password == "" {
		return nil, errs.New(errs.CodeUsage,
			"a demo operator account needs a password from REPAIRFLOW_NEW_PASSWORD (never from command-line arguments)")
	}
	users, err := svc.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	for _, u := range users {
		if u.Username == username {
			return &UserResult{Username: u.Username, Role: u.Role, AlreadyExisted: true}, nil
		}
	}
	created, err := svc.CreateUser(ctx, DemoActor, username, opts.Password, DemoUserRole)
	if err != nil {
		return nil, err
	}
	return &UserResult{Username: created.Username, Role: created.Role, Created: true}, nil
}
