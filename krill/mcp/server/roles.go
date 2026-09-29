package server

// RoleConfig names the Keycloak realm roles that map to krill personas.
// An empty name means "no identity holds this persona": an unset operator
// role makes nobody an operator, never everybody.
type RoleConfig struct {
	OperatorRole string
	ReaderRole   string
}

// ResolvePersona maps an identity's realm_access.roles to a persona. It is
// flat and explicit: operator iff it holds OperatorRole, reader iff it
// holds ReaderRole, and operator implies read. ok is false when the
// identity holds neither -- callers must reject, never default.
func (c RoleConfig) ResolvePersona(roles []string) (persona Persona, ok bool) {
	var isOperator, isReader bool
	for _, r := range roles {
		if c.OperatorRole != "" && r == c.OperatorRole {
			isOperator = true
		}
		if c.ReaderRole != "" && r == c.ReaderRole {
			isReader = true
		}
	}
	switch {
	case isOperator:
		return PersonaSwarmOperator, true
	case isReader:
		return PersonaReader, true
	}
	return "", false
}
