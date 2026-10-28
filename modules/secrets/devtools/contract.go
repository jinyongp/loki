package devtools

// The private broker adapter consumes a separate reviewed CLI surface from the
// coordination module. These constants never permit a generic CLI operation.
const (
	DevtoolsVersion        = "0.37.0"
	DevtoolsCommit         = "f8c22ac3674a14c8d57ed919e866fc104d6d7b5f"
	DevtoolsProtocol       = 19
	RunnerWireProtocol     = 1
	RedactionProtocol      = 1
	MaxPrivateControlFrame = 128 << 10
	MaxPrivatePayloadFrame = 16 << 20
	MaxProtectedJobs       = 8
	MaxRawStreamQueue      = 64 << 10
	MaxRequestLedgerBytes  = 64 << 20
)

type DevtoolsAuthority string

const (
	DevtoolsMetadata DevtoolsAuthority = "metadata"
	DevtoolsAdmin    DevtoolsAuthority = "admin"
	DevtoolsJobs     DevtoolsAuthority = "jobs"
)

// AllowsDevtoolsCommand is an additional guard inside the broker. The public
// transports expose typed operations, never this command selector or argv.
func AllowsDevtoolsCommand(authority DevtoolsAuthority, command string) bool {
	if authority != DevtoolsMetadata && authority != DevtoolsAdmin && authority != DevtoolsJobs {
		return false
	}
	switch command {
	case "values inspect", "values revision", "values request show", "vault status", "var list", "secret list", "env list", "bundle list":
		return authority == DevtoolsMetadata || authority == DevtoolsAdmin
	case "values runner address", "values runner show", "values runner register", "values runner unregister", "values execution prepare", "values execution approve", "values execution deliver", "values execution report", "values execution cancel", "values execution status":
		return authority == DevtoolsJobs
	case "var set", "var unset", "secret set", "secret unset", "secret generate", "import", "env create", "env remove", "bundle set", "bundle rename", "bundle remove", "vault init", "vault unlock", "vault lock", "vault recover", "vault rewrap", "profile export", "profile import":
		return authority == DevtoolsAdmin
	}
	return false
}
