package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildInviteWelcomeURL(t *testing.T) {
	t.Setenv("APP_URL", "https://preview.example.com")

	url := buildInviteWelcomeURL("invite-token-123")
	assert.Equal(t, "https://preview.example.com/welcome/invite?invite_token=invite-token-123", url)
}

func TestBuildInviteWelcomeURLEncodesToken(t *testing.T) {
	t.Setenv("APP_URL", "https://preview.example.com")

	url := buildInviteWelcomeURL("token with spaces")
	assert.Equal(t, "https://preview.example.com/welcome/invite?invite_token=token+with+spaces", url)
}

func TestBuildInviteWelcomeURLWithTrailingSlash(t *testing.T) {
	t.Setenv("APP_URL", "https://preview.example.com/")

	url := buildInviteWelcomeURL("token-123")
	assert.Equal(t, "https://preview.example.com/welcome/invite?invite_token=token-123", url)
}

func TestBuildInviteWelcomeURLDefaultAppURL(t *testing.T) {
	t.Setenv("APP_URL", "")

	url := buildInviteWelcomeURL("token-123")
	assert.Equal(t, "https://hover.app.goodnative.co/welcome/invite?invite_token=token-123", url)
}
