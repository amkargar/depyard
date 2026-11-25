import type { Component } from 'vue'
import PubRegistryExplorer from '~/components/explorers/pub/PubRegistryExplorer.vue'
import PubPackageDetail from '~/components/explorers/pub/PubPackageDetail.vue'

export interface RegistryExplorerMapping {
  label: string
  explorer: Component
  detail: Component
}

/**
 * Small static mapping from compiled registry type to its isolated ecosystem-specific UI.
 * Future ecosystems (e.g. go, rpm, npm) register their own isolated explorer components here
 * without forcing every ecosystem into the Pub layout or introducing a dynamic plugin engine.
 */
export const REGISTRY_EXPLORERS: Record<string, RegistryExplorerMapping> = {
  pub: {
    label: 'Dart / Flutter Pub',
    explorer: PubRegistryExplorer,
    detail: PubPackageDetail,
  },
}

export function getRegistryExplorer(type: string): RegistryExplorerMapping | null {
  return REGISTRY_EXPLORERS[type.toLowerCase()] ?? null
}
