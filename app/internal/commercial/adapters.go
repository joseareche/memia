package commercial

// Pluggable adapters, DISABLED by default. Only interfaces + config stubs: no network calls are made
// until the user provides the service (RUC API token, SMTP) and an implementation is enabled.

import (
	"context"
	"errors"
)

var ErrAdapterDisabled = errors.New("adapter not configured")

// RUCInfo is what a RUC lookup may return. Fields are never invented: empty means unknown.
type RUCInfo struct {
	RUC, LegalName, Address, Status, Condition, Source string
}

// RUCLookup resolves a RUC against SUNAT data (e.g. apis.net.pe / decolecta with token, or an offline
// "padrón reducido" import). Implementations must return ErrAdapterDisabled when not configured.
type RUCLookup interface {
	Key() string
	Enabled() bool
	Lookup(ctx context.Context, ruc string) (*RUCInfo, error)
}

// Mailer sends a quote export. Recipient allowlist and an explicit user click are enforced by the caller.
type Mailer interface {
	Key() string
	Enabled() bool
	Send(ctx context.Context, to, subject, body string, attachName string, attach []byte) error
}

type disabledRUC struct{}

func (disabledRUC) Key() string   { return "sunat-ruc (no configurado)" }
func (disabledRUC) Enabled() bool { return false }
func (disabledRUC) Lookup(context.Context, string) (*RUCInfo, error) {
	return nil, ErrAdapterDisabled
}

type disabledMailer struct{}

func (disabledMailer) Key() string   { return "smtp (no configurado)" }
func (disabledMailer) Enabled() bool { return false }
func (disabledMailer) Send(context.Context, string, string, string, string, []byte) error {
	return ErrAdapterDisabled
}

// Adapter config stubs (what the user must provide), surfaced in /v1/commercial/integrations.
var adapterNeeds = []map[string]any{
	{"kind": "ruc_lookup", "key": "sunat-ruc", "enabled": false, "status": "not_configured",
		"needs": []string{"Proveedor (p. ej. apis.net.pe / decolecta) y token de API", "o bien archivo «padrón reducido» SUNAT para importación offline"}},
	{"kind": "mail", "key": "smtp", "enabled": false, "status": "not_configured",
		"needs": []string{"Servidor SMTP y puerto (587 STARTTLS / 465 TLS)", "Usuario y contraseña (se guardarán cifrados)", "Remitente (From) verificado", "Lista de destinatarios permitidos (inicial: admin@example.com)"}},
}
