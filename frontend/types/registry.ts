export type RepositoryMode = 'proxy' | 'hosted' | 'hybrid'
export type Visibility = 'public' | 'private'
export type PermissionScope = 'read' | 'publish' | 'delete' | 'admin'
export type UpstreamAuthType = '' | 'none' | 'bearer' | 'basic' | 'header'

export interface Capabilities {
  proxy: boolean
  hosted: boolean
  publish: boolean
  search: boolean
  list_versions: boolean
  cache_warm: boolean
  metadata_sync: boolean
  artifact_deletion: boolean
  version_retraction: boolean
  package_ownership: boolean
  signature_verification: boolean
}

export interface ConfigFieldHint {
  key: string
  label: string
  type: 'string' | 'number' | 'boolean' | 'string_list'
  description?: string
  default_value?: any
}

export interface RegistryTypeInfo {
  type: string
  default_upstream_url?: string
  display_name: string
  description: string
  capabilities: Capabilities
  supported_modes: RepositoryMode[]
  config_hints?: ConfigFieldHint[]
}

export interface AuthPolicy {
  allow_anonymous_read: boolean
  require_auth_publish: boolean
}

export interface CachePolicy {
  metadata_ttl_seconds: number
  archive_ttl_seconds: number
  max_size_bytes: number
  eviction_policy: string
  negative_cache_ttl_seconds: number
}

export interface UpstreamConfig {
  auth_type?: UpstreamAuthType
  auth_header?: string
  auth_username?: string
  auth_token?: string
  has_auth_secret?: boolean
  proxy_url?: string
  timeout_seconds?: number
  insecure_skip_verify?: boolean
  custom_headers?: Record<string, string>
}

export interface RegistryStorageStats {
  registry_id: string
  logical_bytes: number
  hosted_bytes: number
  cached_bytes: number
  total_refs: number
  hosted_refs: number
  cached_refs: number
}

export interface UpstreamHealth {
  checked: boolean
  available: boolean
  status_code?: number
  latency_ms: number
  error?: string
  checked_at?: string
}

export interface RegistryMetricsSnapshot {
  registry_id: string
  downloads: number
  uploads: number
  bytes_served: number
  bytes_received: number
  cache_hits: number
  cache_misses: number
  cache_hit_ratio: number
  upstream_errors: number
  active_transfers: number
  upstream: UpstreamHealth
}

export interface RegistryInstance {
  id: string
  name: string
  slug: string
  type: string
  description: string
  enabled: boolean
  mode: RepositoryMode
  upstream_url?: string
  upstream_config?: UpstreamConfig
  visibility: Visibility
  auth_policy: AuthPolicy
  storage_quota_bytes: number
  cache_policy: CachePolicy
  config?: Record<string, any>
  is_default: boolean
  hostname?: string
  created_at: string
  updated_at: string
  // Enriched fields returned by Admin API
  installed: boolean
  base_path: string
  health: 'healthy' | 'degraded' | 'unhealthy' | 'disabled'
  type_capabilities: Capabilities
  effective_capabilities: Capabilities
  storage: RegistryStorageStats
  quota_utilization: number
  metrics: RegistryMetricsSnapshot
  artifact_count: number
}

export interface PublicRegistryView {
  id: string
  name: string
  slug: string
  type: string
  description: string
  mode: RepositoryMode
  visibility: Visibility
  is_default: boolean
  base_path: string
  capabilities: Capabilities
  artifact_count: number
  downloads: number
}

export interface ArtifactDescriptor {
  id: string
  registry_id: string
  name: string
  version?: string
  version_count?: number
  size_bytes: number
  digest?: string
  origin: string
  description?: string
  downloads?: number
  retracted?: boolean
  created_at: string
  updated_at: string
}

export interface ArtifactPage {
  items: ArtifactDescriptor[]
  total: number
  page: number
  limit: number
}

export interface GlobalStorageStats {
  physical_bytes: number
  physical_blob_count: number
  logical_bytes: number
  logical_ref_count: number
  deduplication_saved_bytes: number
  deduplication_ratio: number
}

export interface BucketRecord {
  id: string
  registry_id: string
  bucket_date: string
  downloads: number
  uploads: number
  bytes_served: number
  bytes_received: number
  cache_hits: number
  cache_misses: number
  upstream_errors: number
}

export interface StatsOverview {
  storage: GlobalStorageStats
  downloads: number
  uploads: number
  bytes_served: number
  bytes_received: number
  cache_hits: number
  cache_misses: number
  cache_hit_ratio: number
  active_transfers: number
  registry_count: number
  installed_types: RegistryTypeInfo[]
  registries: RegistryInstance[]
  history: BucketRecord[]
}

export interface AccessToken {
  id: string
  name: string
  prefix: string
  user_id: string
  registry_id?: string
  registry_slug?: string
  registry_type?: string
  scopes: PermissionScope[]
  namespaces?: string[]
  expires_at?: string
  last_used_at?: string
  revoked: boolean
  created_at: string
}

export interface User {
  id: string
  username: string
  email: string
  role: 'admin' | 'user'
  disabled: boolean
  created_at: string
  updated_at: string
}

export interface AuditEntry {
  id: string
  timestamp: string
  actor_id?: string
  actor_name: string
  actor_type: string
  action: string
  registry_id?: string
  target?: string
  ip_address?: string
  success: boolean
  details?: Record<string, any>
}

/** One FLUTTER_STORAGE_BASE_URL category (flutter_infra_release, download.flutter.io, ...). */
export interface FlutterDownloadStorageSetting {
  name: string
  upstream_url?: string
  allowed_prefixes?: string[]
  manifest_ttl_seconds?: number
  rewrite_manifest_urls?: boolean
  enabled?: boolean
}

export interface FlutterStorageChannel {
  name: string
  enabled: boolean
  upstream_url: string
  mirror_url: string
  allowed_prefixes: string[]
  rewrites_manifests: boolean
  manifest_ttl_seconds: number
  inherits_upstream: boolean
}

export interface FlutterVersionDownload {
  platform: string
  archive: string
  mirror_url: string
  sha256?: string
}

export interface FlutterVersionStorage {
  category: string
  path: string
  upstream_url: string
  mirror_url: string
  inherited_from?: string
}

export interface FlutterVersionEntry {
  version: string
  channel: string
  dart_sdk_version?: string
  release_date?: string
  hash?: string
  archived: boolean
  downloads: FlutterVersionDownload[]
  storage: FlutterVersionStorage[]
  sdk_download_url?: string
  dart_sdk_url?: string
  engine_manifest_url?: string
  registry_entry_url: string
}

export interface FlutterVersionChannel {
  name: string
  current_release?: string
  manifest_url?: string
  managed_by?: string
}

export interface FlutterVersionRegistry {
  registry_id: string
  registry_slug: string
  storage_name: string
  upstream_url: string
  releases_base_url: string
  base_path: string
  generated_at: string
  expires_at: string
  current_release?: string
  channels: FlutterVersionChannel[]
  storage: FlutterStorageChannel[]
  versions: FlutterVersionEntry[]
  total_versions: number
  warnings?: string[]
}

export interface PlatformSettings {
  id: string
  site_title: string
  public_base_url: string
  enable_root_compat_route: boolean
  audit_retention_days: number
  stats_retention_days: number
  upstream_proxy_url: string
  upstream_timeout_seconds: number
  upstream_user_agent: string
  upstream_insecure_skip_verify: boolean
  default_pub_upstream_url: string
  default_flutter_storage_url: string
  /** Platform-wide base Flutter Storage config (seeds new registries / feeds inheritance). */
  default_flutter_storage_config?: Record<string, any>
  /** "defaults" = seed + manual apply only; "inherit" = unset registry fields follow the base live. */
  flutter_base_config_mode?: 'defaults' | 'inherit'
  updated_at: string
}

/** One field's resolution inside a base-config diff. */
export interface FlutterBaseFieldDiff {
  key: string
  effective?: unknown
  base?: unknown
  source: 'registry' | 'base' | 'default'
  can_revert: boolean
  in_sync: boolean
  categories?: Array<{
    name: string
    source: 'registry' | 'base' | 'registry+base' | 'default'
    overrides?: string[]
    inherits?: string[]
  }>
}

export interface BaseConfigFieldChange {
  key: string
  from?: unknown
  to: unknown
}

export interface BaseConfigRegistryImpact {
  id: string
  slug: string
  name: string
  changes: BaseConfigFieldChange[]
  skipped?: string[]
  applied: boolean
  error?: string
  field_sources?: FlutterBaseFieldDiff[]
}

export interface BaseConfigImpact {
  type: string
  mode: 'fill-missing' | 'overwrite'
  dry_run: boolean
  registries: BaseConfigRegistryImpact[]
  applied: number
  failed: number
}
