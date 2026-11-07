package main

import (
	"testing"
)

func TestLoginCmdHelp(t *testing.T) {
	if loginCmd.Use != "login [provider]" {
		t.Errorf("loginCmd.Use = %q, want 'login [provider]'", loginCmd.Use)
	}
	if logoutCmd.Use != "logout [provider]" {
		t.Errorf("logoutCmd.Use = %q, want 'logout [provider]'", logoutCmd.Use)
	}
}
