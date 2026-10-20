package commercial

import (
	"testing"
	"time"
)

func TestActorSignature(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	now := time.Unix(1_800_000_000, 0)
	h := SignActor(key, "admin@example.com", now)
	if e, ok := VerifyActor(key, h, now.Add(30*time.Second)); !ok || e != "admin@example.com" {
		t.Fatal("valid signature rejected")
	}
	if _, ok := VerifyActor(key, h, now.Add(5*time.Minute)); ok {
		t.Fatal("stale signature accepted")
	}
	if _, ok := VerifyActor([]byte("ffffffffffffffffffffffffffffffff"), h, now); ok {
		t.Fatal("wrong key accepted")
	}
	forged := "v1.attacker@x.com" + h[len("v1.admin@example.com"):]
	if _, ok := VerifyActor(key, forged, now); ok {
		t.Fatal("email swap accepted")
	}
	if _, ok := VerifyActor([]byte("short"), SignActor([]byte("short"), "a@b.c", now), now); ok {
		t.Fatal("short key accepted")
	}
}

func TestRolesAndTransitions(t *testing.T) {
	if Can("viewer", PermEdit) || !Can("seller", PermEdit) || Can("seller", PermApprove) || !Can("approver", PermApprove) || Can("approver", PermSettings) || !Can("admin", PermSettings) || Can("", PermView) {
		t.Fatal("role matrix wrong")
	}
	ok := [][3]string{{"request-approval", "draft", "pending_approval"}, {"approve", "pending_approval", "approved"}, {"return", "pending_approval", "draft"},
		{"send", "approved", "sent"}, {"accept", "sent", "accepted"}, {"reject", "sent", "rejected"}, {"cancel", "draft", "cancelled"}, {"revise", "approved", "draft"}, {"expire", "sent", "expired"}}
	for _, c := range ok {
		if to, _, good := Next(c[0], c[1]); !good || to != c[2] {
			t.Errorf("%v failed", c)
		}
	}
	bad := [][2]string{{"approve", "draft"}, {"send", "draft"}, {"accept", "approved"}, {"request-approval", "approved"}, {"revise", "draft"}, {"cancel", "accepted"}, {"x", "draft"}, {"approve", "cancelled"}}
	for _, c := range bad {
		if _, _, good := Next(c[0], c[1]); good {
			t.Errorf("%v allowed", c)
		}
	}
}
