package assets

import (
	_ "embed"
)

// ProvidersJSON contains all catwalk provider configurations
//go:embed providers.json
var ProvidersJSON []byte
