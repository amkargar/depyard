import type { Component } from 'vue'
import PubRegistryExplorer from '~/components/explorers/pub/PubRegistryExplorer.vue'
import PubPackageDetail from '~/components/explorers/pub/PubPackageDetail.vue'
import FlutterStorageExplorer from '~/components/explorers/flutter/FlutterStorageExplorer.vue'
import GoRegistryExplorer from '~/components/explorers/go/GoRegistryExplorer.vue'
import GoModuleDetail from '~/components/explorers/go/GoModuleDetail.vue'
import PypiRegistryExplorer from '~/components/explorers/pypi/PypiRegistryExplorer.vue'
import PypiProjectDetail from '~/components/explorers/pypi/PypiProjectDetail.vue'
import NpmRegistryExplorer from '~/components/explorers/npm/NpmRegistryExplorer.vue'
import NpmPackageDetail from '~/components/explorers/npm/NpmPackageDetail.vue'

export interface RegistryExplorerMapping {
  label: string
  explorer: Component
  detail?: Component
}

/**
 * Small static mapping from compiled registry type to its isolated ecosystem-specific UI.
 * Future ecosystems register their own isolated explorer components here
 * without forcing every ecosystem into the Pub layout or introducing a dynamic plugin engine.
 */
export const REGISTRY_EXPLORERS: Record<string, RegistryExplorerMapping> = {
  pub: {
    label: 'Dart / Flutter Pub',
    explorer: PubRegistryExplorer,
    detail: PubPackageDetail,
  },
  flutter: {
    label: 'Flutter Storage Mirror',
    explorer: FlutterStorageExplorer,
  },
  goproxy: {
    label: 'Go Module Proxy',
    explorer: GoRegistryExplorer,
    detail: GoModuleDetail,
  },
  pypi: {
    label: 'Python Package Index',
    explorer: PypiRegistryExplorer,
    detail: PypiProjectDetail,
  },
  npm: {
    label: 'npm Registry',
    explorer: NpmRegistryExplorer,
    detail: NpmPackageDetail,
  },
}

export function getRegistryExplorer(type: string): RegistryExplorerMapping | null {
  return REGISTRY_EXPLORERS[type.toLowerCase()] ?? null
}
