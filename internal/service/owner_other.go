//go:build !unix

package service

// ownerUser is unavailable off Unix; the OpenRC script then runs without a
// command_user line (it never targets such a host in practice).
func ownerUser(string) string { return "" }
