package commercial

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

const (
	PermView     = "quotes.view"
	PermEdit     = "quotes.edit" // create/edit/request approval/send/mark outcome/cancel/revise
	PermApprove  = "quotes.approve"
	PermCustomer = "customers.manage"
	PermSettings = "settings.manage"
)

var rolePerms = map[string]map[string]bool{
	"viewer":   {PermView: true},
	"seller":   {PermView: true, PermEdit: true, PermCustomer: true},
	"approver": {PermView: true, PermEdit: true, PermCustomer: true, PermApprove: true},
	"admin":    {PermView: true, PermEdit: true, PermCustomer: true, PermApprove: true, PermSettings: true},
}

func Can(role, perm string) bool { return rolePerms[role][perm] }

// ActorHeader carries the end user identity from the chat service:
//
//	"v1.<email>.<unix-seconds>.<hex hmac-sha256(key, "v1|email|ts")>"
//
// The email may contain dots, so it is parsed from both ends.
const ActorHeader = "X-DevBrain-Actor"

func SignActor(key []byte, email string, ts time.Time) string {
	sec := strconv.FormatInt(ts.Unix(), 10)
	m := hmac.New(sha256.New, key)
	m.Write([]byte("v1|" + email + "|" + sec))
	return "v1." + email + "." + sec + "." + hex.EncodeToString(m.Sum(nil))
}

// VerifyActor returns the lower-cased email if the signature is valid and fresh (±120 s).
func VerifyActor(key []byte, h string, now time.Time) (string, bool) {
	if len(key) < 32 || !strings.HasPrefix(h, "v1.") {
		return "", false
	}
	rest := h[3:]
	i := strings.LastIndexByte(rest, '.')
	if i < 0 {
		return "", false
	}
	if len(rest)-i-1 != 64 {
		return "", false
	}
	rest = rest[:i]
	j := strings.LastIndexByte(rest, '.')
	if j < 0 {
		return "", false
	}
	email, sec := rest[:j], rest[j+1:]
	ts, err := strconv.ParseInt(sec, 10, 64)
	if err != nil || email == "" || !strings.Contains(email, "@") {
		return "", false
	}
	if d := now.Unix() - ts; d > 120 || d < -120 {
		return "", false
	}
	want := SignActor(key, email, time.Unix(ts, 0))
	if !hmac.Equal([]byte(want), []byte(h)) {
		return "", false
	}
	return strings.ToLower(email), true
}
