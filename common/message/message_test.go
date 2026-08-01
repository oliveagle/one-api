package message

import (
	"strings"
	"testing"

	"github.com/songquanpeng/one-api/common/config"
)

func TestEmailTemplate_ContainsTitle(t *testing.T) {
	oldName := config.SystemName
	config.SystemName = "Test System"
	t.Cleanup(func() { config.SystemName = oldName })

	html := EmailTemplate("Welcome", "Hello, user!")
	if !strings.Contains(html, "Welcome") {
		t.Error("EmailTemplate should contain the title")
	}
	if !strings.Contains(html, "Hello, user!") {
		t.Error("EmailTemplate should contain the content")
	}
	if !strings.Contains(html, "Test System") {
		t.Error("EmailTemplate should contain the system name")
	}
}

func TestEmailTemplate_HtmlStructure(t *testing.T) {
	html := EmailTemplate("Title", "Content")
	if !strings.Contains(html, "<!DOCTYPE html>") {
		t.Error("EmailTemplate should contain DOCTYPE")
	}
	if !strings.Contains(html, "</html>") {
		t.Error("EmailTemplate should contain html closing tag")
	}
}

func TestNotify_UnknownMethod(t *testing.T) {
	err := Notify("unknown_method", "title", "desc", "content")
	if err == nil {
		t.Fatal("expected error for unknown notify method")
	}
	if !strings.Contains(err.Error(), "unknown notify method") {
		t.Errorf("error = %q, want 'unknown notify method'", err.Error())
	}
}

func TestShouldAuth_NoCredentials(t *testing.T) {
	oldAccount := config.SMTPAccount
	oldToken := config.SMTPToken
	config.SMTPAccount = ""
	config.SMTPToken = ""
	t.Cleanup(func() {
		config.SMTPAccount = oldAccount
		config.SMTPToken = oldToken
	})

	if shouldAuth() {
		t.Error("shouldAuth should be false when no credentials are set")
	}
}

func TestShouldAuth_WithCredentials(t *testing.T) {
	oldAccount := config.SMTPAccount
	oldToken := config.SMTPToken
	config.SMTPAccount = "user@example.com"
	config.SMTPToken = "token123"
	t.Cleanup(func() {
		config.SMTPAccount = oldAccount
		config.SMTPToken = oldToken
	})

	if !shouldAuth() {
		t.Error("shouldAuth should be true when credentials are set")
	}
}

func TestSendEmail_EmptyReceiver(t *testing.T) {
	err := SendEmail("subject", "", "content")
	if err == nil {
		t.Fatal("expected error for empty receiver")
	}
	if !strings.Contains(err.Error(), "receiver is empty") {
		t.Errorf("error = %q, want 'receiver is empty'", err.Error())
	}
}
