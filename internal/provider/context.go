package provider

import "context"

// Ctx is the context the provider methods take. It exists so the package does
// not need "context" only to spell the parameter type in its interface.
type Ctx = context.Context
