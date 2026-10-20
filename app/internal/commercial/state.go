package commercial

// Quote state machine, validated in the backend. Editing is only allowed in draft.
type transition struct {
	from []string
	to   string
	perm string
}

var Transitions = map[string]transition{
	"request-approval": {[]string{"draft"}, "pending_approval", PermEdit},
	"approve":          {[]string{"pending_approval"}, "approved", PermApprove}, // assigns number + locked version
	"return":           {[]string{"pending_approval"}, "draft", PermApprove},    // internal rejection back to draft
	"send":             {[]string{"approved"}, "sent", PermEdit},                // marks as sent (no email in Phase A)
	"accept":           {[]string{"sent"}, "accepted", PermEdit},
	"reject":           {[]string{"sent"}, "rejected", PermEdit}, // customer rejected
	"expire":           {[]string{"approved", "sent"}, "expired", PermEdit},
	"cancel":           {[]string{"draft", "pending_approval", "approved", "sent"}, "cancelled", PermEdit},
	"revise":           {[]string{"approved", "sent", "rejected", "expired"}, "draft", PermEdit}, // new version, keeps number
}

// Next returns the target state or an error message in Spanish.
func Next(action, from string) (string, string, bool) {
	t, ok := Transitions[action]
	if !ok {
		return "", "acción no válida", false
	}
	for _, f := range t.from {
		if f == from {
			return t.to, "", true
		}
	}
	return "", "transición no permitida desde el estado " + from, false
}
