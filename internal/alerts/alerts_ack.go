package alerts

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	"golang.org/x/crypto/ssh"
)

// Alert acknowledgement
//
// The user of an open alert history row acknowledges it with
// POST /api/beszel/alerts-history/{id}/ack (optionally with a note), or with
// the signed one-click link in the alert's notification,
// GET /api/beszel/ack/{token}, a confirmation page whose form POSTs to the
// same URL to acknowledge. Acknowledged alerts get no reminders.
// Readonly users cannot acknowledge.
//
// A link token is "<historyID>.<userID>.<expiry unix seconds>.<signature>",
// the signature being the base64url HMAC-SHA256 of the first three parts
// with a key derived with HKDF-SHA256 from the seed of the hub's ed25519
// private key. Links expire after ackLinkTTL.
const (
	ackLinkTTL  = 7 * 24 * time.Hour
	ackHKDFInfo = "infrascope/ack/v1"
	// ackNoteMaxChars is the maximum length of an acknowledgement note.
	ackNoteMaxChars = 1000
	// hubKeyFileName is the hub's private key file in the data dir.
	hubKeyFileName = "id_ed25519"
)

var (
	errAckNotFound    = errors.New("alert not found")
	errAckInvalidLink = errors.New("invalid or expired acknowledgement link")
)

// ackKeys caches the derived link keys by key file path.
var ackKeys sync.Map

// ackKey returns the key that signs acknowledgement links. It is derived
// from the hub key in the data dir and is an error while that key is missing.
func (am *AlertManager) ackKey() ([]byte, error) {
	if am.ackSecret != nil {
		return am.ackSecret, nil
	}
	path := filepath.Join(am.hub.DataDir(), hubKeyFileName)
	if key, ok := ackKeys.Load(path); ok {
		return key.([]byte), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw, err := ssh.ParseRawPrivateKey(data)
	if err != nil {
		return nil, err
	}
	var seed []byte
	switch key := raw.(type) {
	case ed25519.PrivateKey:
		seed = key.Seed()
	case *ed25519.PrivateKey:
		seed = key.Seed()
	default:
		return nil, fmt.Errorf("unsupported hub key type %T", raw)
	}
	key, err := deriveAckKey(seed)
	if err != nil {
		return nil, err
	}
	ackKeys.Store(path, key)
	return key, nil
}

func deriveAckKey(secret []byte) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errors.New("empty key material")
	}
	return hkdf.Key(sha256.New, secret, nil, ackHKDFInfo, 32)
}

func ackSignature(key []byte, payload string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// signAckToken returns a link token for a history row of a user that
// expires at expires.
func signAckToken(key []byte, historyID, userID string, expires time.Time) string {
	payload := historyID + "." + userID + "." + strconv.FormatInt(expires.Unix(), 10)
	return payload + "." + ackSignature(key, payload)
}

// verifyAckToken returns the history row and user of a valid, unexpired token.
func verifyAckToken(key []byte, token string, now time.Time) (historyID, userID string, err error) {
	if len(token) > 256 {
		return "", "", errAckInvalidLink
	}
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" {
		return "", "", errAckInvalidLink
	}
	payload := strings.Join(parts[:3], ".")
	if !hmac.Equal([]byte(parts[3]), []byte(ackSignature(key, payload))) {
		return "", "", errAckInvalidLink
	}
	expires, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || !now.Before(time.Unix(expires, 0)) {
		return "", "", errAckInvalidLink
	}
	return parts[0], parts[1], nil
}

// ackLink returns the one-click acknowledgement link of a history row for
// the notifications of userID, or "" when none can be made: without an app
// URL, without a hub key, or for readonly users.
func (am *AlertManager) ackLink(historyID, userID string, now time.Time) string {
	if historyID == "" || strings.TrimSpace(am.hub.Settings().Meta.AppURL) == "" {
		return ""
	}
	user, err := am.hub.FindRecordById("users", userID)
	if err != nil || user.GetString("role") == "readonly" {
		return ""
	}
	key, err := am.ackKey()
	if err != nil {
		return ""
	}
	return am.hub.MakeLink("api", "beszel", "ack", signAckToken(key, historyID, userID, now.Add(ackLinkTTL)))
}

// openHistoryID returns the id of the open history row of an alerts record
// for a user, or "".
func openHistoryID(app core.App, alertID, userID string) string {
	record, err := app.FindFirstRecordByFilter("alerts_history",
		"alert_id={:alert} && user={:user} && resolved=null",
		dbx.Params{"alert": alertID, "user": userID})
	if err != nil {
		return ""
	}
	return record.Id
}

// acknowledgeHistory acknowledges a history row of userID. A row that is
// already acknowledged keeps its acknowledgement unless a note is given,
// which replaces the note. It returns whether the row changed.
func acknowledgeHistory(app core.App, id, userID, note string, now time.Time) (record *core.Record, changed bool, err error) {
	err = app.RunInTransaction(func(tx core.App) error {
		record, err = tx.FindRecordById("alerts_history", id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && record.GetString("user") != userID) {
			return errAckNotFound
		}
		if err != nil {
			return err
		}
		if !record.GetDateTime("acknowledgedAt").IsZero() {
			if note == "" || note == record.GetString("ackNote") {
				return nil
			}
		} else {
			record.Set("acknowledgedAt", now.UTC())
			record.Set("acknowledgedBy", userID)
		}
		record.Set("ackNote", note)
		changed = true
		return tx.Save(record)
	})
	return record, changed, err
}

// unacknowledgeHistory clears the acknowledgement of a history row of userID.
func unacknowledgeHistory(app core.App, id, userID string) (record *core.Record, err error) {
	err = app.RunInTransaction(func(tx core.App) error {
		record, err = tx.FindRecordById("alerts_history", id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && record.GetString("user") != userID) {
			return errAckNotFound
		}
		if err != nil {
			return err
		}
		if record.GetDateTime("acknowledgedAt").IsZero() {
			return nil
		}
		record.Set("acknowledgedAt", nil)
		record.Set("acknowledgedBy", "")
		record.Set("ackNote", "")
		return tx.Save(record)
	})
	return record, err
}

func ackResponse(record *core.Record) map[string]any {
	var at any
	if dt := record.GetDateTime("acknowledgedAt"); !dt.IsZero() {
		at = dt.String()
	}
	return map[string]any{
		"id":             record.Id,
		"acknowledgedAt": at,
		"acknowledgedBy": record.GetString("acknowledgedBy"),
		"ackNote":        record.GetString("ackNote"),
	}
}

// requireAckAuth rejects unauthenticated and readonly requests.
func requireAckAuth(e *core.RequestEvent) error {
	if e.Auth == nil {
		return e.UnauthorizedError("The request requires valid record authorization token.", nil)
	}
	if e.Auth.GetString("role") == "readonly" {
		return e.ForbiddenError("The authorized record is not allowed to perform this action.", nil)
	}
	return nil
}

// AcknowledgeAlert handles POST /api/beszel/alerts-history/{id}/ack with an
// optional {"note": "..."}.
func (am *AlertManager) AcknowledgeAlert(e *core.RequestEvent) error {
	if err := requireAckAuth(e); err != nil {
		return err
	}
	var body struct {
		Note string `json:"note"`
	}
	if e.Request.ContentLength != 0 {
		if err := e.BindBody(&body); err != nil {
			return e.BadRequestError("Invalid request body.", nil)
		}
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > ackNoteMaxChars {
		return e.BadRequestError(fmt.Sprintf("The note must be at most %d characters.", ackNoteMaxChars), nil)
	}
	record, _, err := acknowledgeHistory(e.App, e.Request.PathValue("id"), e.Auth.Id, note, time.Now())
	if errors.Is(err, errAckNotFound) {
		return e.NotFoundError("Alert not found.", nil)
	}
	if err != nil {
		return err
	}
	return e.JSON(http.StatusOK, ackResponse(record))
}

// UnacknowledgeAlert handles POST /api/beszel/alerts-history/{id}/unack.
func (am *AlertManager) UnacknowledgeAlert(e *core.RequestEvent) error {
	if err := requireAckAuth(e); err != nil {
		return err
	}
	record, err := unacknowledgeHistory(e.App, e.Request.PathValue("id"), e.Auth.Id)
	if errors.Is(err, errAckNotFound) {
		return e.NotFoundError("Alert not found.", nil)
	}
	if err != nil {
		return err
	}
	return e.JSON(http.StatusOK, ackResponse(record))
}

const ackLinkInvalidPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Link expired</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5">
<h1 style="font-size:1.25rem">This acknowledgement link is invalid or has expired</h1>
<p>Open InfraScope to acknowledge the alert from the alert history instead.</p>
</body></html>`

// ackConfirmPage is the confirmation page of an acknowledgement link; %s is
// the HTML-escaped alert title. The form posts to the same URL, whose token
// is the capability, so no cookies or other state are involved.
const ackConfirmPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<title>Acknowledge alert</title></head>
<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem;line-height:1.5">
<h1 style="font-size:1.25rem">Acknowledge alert</h1>
<p><strong>%s</strong></p>
<p>Acknowledging stops reminders for this alert.</p>
<form method="post"><button type="submit" style="font:inherit;padding:.5rem 1rem;cursor:pointer">Acknowledge</button></form>
</body></html>`

// verifyAckLink returns the history row and user of a valid link token of a
// user that may acknowledge, or an error.
func (am *AlertManager) verifyAckLink(e *core.RequestEvent) (*core.Record, string, error) {
	key, err := am.ackKey()
	if err != nil {
		return nil, "", err
	}
	historyID, userID, err := verifyAckToken(key, e.Request.PathValue("token"), time.Now())
	if err != nil {
		return nil, "", err
	}
	user, err := e.App.FindRecordById("users", userID)
	if err != nil || user.GetString("role") == "readonly" {
		return nil, "", errAckInvalidLink
	}
	record, err := e.App.FindRecordById("alerts_history", historyID)
	if err != nil || record.GetString("user") != userID {
		return nil, "", errAckInvalidLink
	}
	return record, userID, nil
}

// HandleAckLink handles the acknowledgement link of notifications,
// /api/beszel/ack/{token}. GET only shows a confirmation page (so link
// scanners acknowledge nothing); its form POSTs to the same URL, which
// acknowledges the alert (again posting changes nothing) and redirects to the
// app with ?ack=1. Invalid, expired and readonly links get a 400 page.
func (am *AlertManager) HandleAckLink(e *core.RequestEvent) error {
	header := e.Response.Header()
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	record, userID, err := am.verifyAckLink(e)
	if err != nil {
		return e.HTML(http.StatusBadRequest, ackLinkInvalidPage)
	}
	if e.Request.Method != http.MethodPost {
		var systemName string
		if systemID := record.GetString("system"); systemID != "" {
			if system, err := e.App.FindRecordById("systems", systemID); err == nil {
				systemName = system.GetString("name")
			}
		}
		title := reminderTitle(record.GetString("name"), systemName, record.GetString("monitor_name"))
		return e.HTML(http.StatusOK, fmt.Sprintf(ackConfirmPage, html.EscapeString(title)))
	}
	record, _, err = acknowledgeHistory(e.App, record.Id, userID, "", time.Now())
	if err != nil {
		return e.HTML(http.StatusBadRequest, ackLinkInvalidPage)
	}
	var target string
	switch {
	case record.GetString("monitor") != "":
		target = am.hub.MakeLink("monitors")
	case record.GetString("system") != "":
		target = am.hub.MakeLink("system", record.GetString("system"))
	default:
		target = am.hub.MakeLink()
	}
	if target == "" {
		target = "/"
	}
	return e.Redirect(http.StatusSeeOther, target+"?ack=1")
}
