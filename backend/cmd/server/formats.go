package main

import (
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/cargo"
	"github.com/amkargar/depyard/backend/internal/registry/composer"
	"github.com/amkargar/depyard/backend/internal/registry/conan"
	"github.com/amkargar/depyard/backend/internal/registry/gitlfs"
	"github.com/amkargar/depyard/backend/internal/registry/helm"
	"github.com/amkargar/depyard/backend/internal/registry/huggingface"
	"github.com/amkargar/depyard/backend/internal/registry/layout"
	"github.com/amkargar/depyard/backend/internal/registry/nuget"
	"github.com/amkargar/depyard/backend/internal/registry/oci"
)

// These formats persist their protocol objects in the shared ref/blob stores.
// No ecosystem metadata database or database-specific wiring is required.
func objectFormats() []registry.Registration {
	return append(layout.Registrations(), cargo.Registration(), composer.Registration(), conan.Registration(), gitlfs.Registration(), helm.Registration(), huggingface.Registration(), nuget.Registration(), oci.Registration())
}
