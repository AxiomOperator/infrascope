//go:build testing

package alerts

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAckToken(t *testing.T) {
	key, err := deriveAckKey([]byte("seed"))
	require.NoError(t, err)
	other, err := deriveAckKey([]byte("other seed"))
	require.NoError(t, err)
	now := time.Unix(1_800_000_000, 0)
	token := signAckToken(key, "history1234567", "user1234567890", now.Add(ackLinkTTL))

	history, user, err := verifyAckToken(key, token, now)
	require.NoError(t, err)
	assert.Equal(t, "history1234567", history)
	assert.Equal(t, "user1234567890", user)

	_, _, err = verifyAckToken(key, token, now.Add(ackLinkTTL))
	assert.ErrorIs(t, err, errAckInvalidLink, "expired")
	_, _, err = verifyAckToken(other, token, now)
	assert.ErrorIs(t, err, errAckInvalidLink, "other key")

	parts := strings.Split(token, ".")
	for name, tampered := range map[string]string{
		"history": "history7654321." + strings.Join(parts[1:], "."),
		"user":    parts[0] + ".user0987654321." + strings.Join(parts[2:], "."),
		"expiry":  strings.Join(parts[:2], ".") + ".9999999999." + parts[3],
		"sig":     strings.Join(parts[:3], ".") + ".AAAA",
		"parts":   strings.Join(parts[:3], "."),
		"empty":   "",
		"long":    strings.Repeat("a", 300),
	} {
		_, _, err := verifyAckToken(key, tampered, now)
		assert.ErrorIs(t, err, errAckInvalidLink, name)
	}
}

func TestValidReminderMinutes(t *testing.T) {
	for minutes, want := range map[float64]bool{0: true, 5: true, 45: true, 1440: true, 4: false, 1441: false, -5: false, 7.5: false} {
		assert.Equal(t, want, validReminderMinutes(minutes), minutes)
	}
}

func TestReminderTitle(t *testing.T) {
	assert.Equal(t, "API is down", reminderTitle(alertNameMonitorDown, "", "API"))
	assert.Equal(t, "Connection to web is down", reminderTitle("Status", "web", ""))
	assert.Equal(t, "web CPU alert", reminderTitle("CPU", "web", ""))
	assert.Equal(t, "web 5m load alert", reminderTitle("LoadAvg5", "web", ""))
}
