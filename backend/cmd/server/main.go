package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/amkargar/depyard/backend/internal/admin"
	"github.com/amkargar/depyard/backend/internal/audit"
	"github.com/amkargar/depyard/backend/internal/auth"
	"github.com/amkargar/depyard/backend/internal/cache"
	"github.com/amkargar/depyard/backend/internal/config"
	"github.com/amkargar/depyard/backend/internal/metrics"
	"github.com/amkargar/depyard/backend/internal/registry"
	"github.com/amkargar/depyard/backend/internal/registry/flutter"
	"github.com/amkargar/depyard/backend/internal/registry/goproxy"
	"github.com/amkargar/depyard/backend/internal/registry/npm"
	"github.com/amkargar/depyard/backend/internal/registry/pub"
	"github.com/amkargar/depyard/backend/internal/registry/pypi"
	"github.com/amkargar/depyard/backend/internal/server"
	"github.com/amkargar/depyard/backend/internal/storage"
	memstore "github.com/amkargar/depyard/backend/internal/store/memory"
	mongostore "github.com/amkargar/depyard/backend/internal/store/mongo"
	"github.com/amkargar/depyard/backend/internal/tasks"
	"github.com/amkargar/depyard/backend/internal/upstream"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := config.Load()
	tlsConfig, err := cfg.ListenerConfig()
	if err != nil {
		logger.Error("invalid listener configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Persistence initialization: MongoDB in production, optional memory store for standalone dev/testing.
	var (
		instanceStore registry.InstanceStore
		refStore      storage.RefStore
		authStore     auth.Store
		metricsStore  metrics.Store
		auditStore    audit.Store
		settingsStore admin.SettingsStore
		pubStore      pub.MetadataStore
		flutterStore  flutter.MetadataStore
		goStore       goproxy.MetadataStore
		pypiStore     pypi.MetadataStore
		npmStore      npm.MetadataStore
		closeStore    func(context.Context) error
	)

	if cfg.StorageBackend == "memory" {
		logger.Warn("starting with in-memory metadata store (STORAGE_BACKEND=memory); set STORAGE_BACKEND=mongo for persistent MongoDB storage")
		ms := memstore.New()
		instanceStore = ms
		refStore = ms
		authStore = ms
		metricsStore = ms
		auditStore = ms
		settingsStore = ms
		pubStore = pub.NewMemoryStore()
		flutterStore = flutter.NewMemoryStore()
		goStore = goproxy.NewMemoryStore()
		pypiStore = pypi.NewMemoryStore()
		npmStore = npm.NewMemoryStore()
	} else {
		mongoDB, err := mongostore.Connect(ctx, cfg.MongoURI, cfg.MongoDatabase)
		if err != nil {
			logger.Error("failed to connect to MongoDB", "uri", cfg.MongoURI, "error", err)
			os.Exit(1)
		}
		instanceStore = mongoDB
		refStore = mongoDB
		authStore = mongoDB
		metricsStore = mongoDB
		auditStore = mongoDB
		settingsStore = mongoDB
		closeStore = mongoDB.Close

		ps, err := pub.NewMongoStore(ctx, mongoDB.Database())
		if err != nil {
			logger.Error("failed to initialize pub metadata store", "error", err)
			os.Exit(1)
		}
		pubStore = ps

		fs, err := flutter.NewMongoStore(ctx, mongoDB.Database())
		if err != nil {
			logger.Error("failed to initialize flutter storage metadata store", "error", err)
			os.Exit(1)
		}
		flutterStore = fs

		gs, err := goproxy.NewMongoStore(ctx, mongoDB.Database())
		if err != nil {
			logger.Error("failed to initialize go proxy metadata store", "error", err)
			os.Exit(1)
		}
		goStore = gs

		ps2, err := pypi.NewMongoStore(ctx, mongoDB.Database())
		if err != nil {
			logger.Error("failed to initialize pypi metadata store", "error", err)
			os.Exit(1)
		}
		pypiStore = ps2

		ns, err := npm.NewMongoStore(ctx, mongoDB.Database())
		if err != nil {
			logger.Error("failed to initialize npm metadata store", "error", err)
			os.Exit(1)
		}
		npmStore = ns
		logger.Info("connected to MongoDB", "database", cfg.MongoDatabase)
	}
	if closeStore != nil {
		defer func() {
			shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			_ = closeStore(shutCtx)
		}()
	}

	// Shared ecosystem-agnostic core services
	fsStore, err := storage.NewFSBlobStore(cfg.DataDir)
	if err != nil {
		logger.Error("failed to initialize blob storage", "data_dir", cfg.DataDir, "error", err)
		os.Exit(1)
	}
	storageSvc := storage.NewService(fsStore, refStore)
	cacheCoord := cache.NewCoordinator(storageSvc)
	upstreamClient := upstream.NewClient(cfg.UpstreamDefaultTimeout)
	authSvc := auth.NewService(authStore, cfg.SessionTTL)
	authSvc.SetMinPasswordLength(cfg.MinPasswordLength)
	metricsCol := metrics.NewCollector(metricsStore)
	auditSvc := audit.NewService(auditStore)

	// Bootstrap default admin account on first startup.
	//
	// When no ADMIN_PASSWORD is configured we generate a strong random one and
	// print it once, instead of shipping a well-known default credential that
	// operators are likely to forget to change.
	adminPassword := cfg.AdminPassword
	generatedPassword := false
	if adminPassword == "" {
		adminPassword = auth.GeneratePassword(24)
		generatedPassword = true
	}
	createdAdmin, err := authSvc.EnsureBootstrapAdmin(ctx, cfg.AdminUsername, cfg.AdminEmail, adminPassword)
	if err != nil {
		logger.Error("failed to bootstrap admin account", "error", err)
		os.Exit(1)
	}
	if createdAdmin {
		logger.Info("bootstrapped initial admin user", "username", cfg.AdminUsername)
		if generatedPassword {
			logger.Warn("no ADMIN_PASSWORD configured: a password was generated for the initial admin account",
				"username", cfg.AdminUsername,
				"password", adminPassword,
				"note", "this password is shown only once - store it now and change it after signing in")
		}
	}

	// Composition root: register compiled registry types.
	// liveDefaults is installed once the admin handler exists (see below) so registries can follow
	// the platform base settings without an import cycle between admin and registry.
	liveDefaults := &admin.LiveDefaults{}
	deps := registry.ServiceDeps{
		Storage:  storageSvc,
		Cache:    cacheCoord,
		Upstream: upstreamClient,
		Auth:     authSvc,
		Metrics:  metricsCol,
		Audit:    auditSvc,
		Defaults: liveDefaults,
	}
	regManager := registry.NewManager(instanceStore, deps)

	for _, reg := range append(objectFormats(), pub.Registration(pubStore), flutter.Registration(flutterStore),
		goproxy.Registration(goStore), pypi.Registration(pypiStore), npm.Registration(npmStore)) {
		if err := regManager.RegisterType(reg); err != nil {
			logger.Error("failed to register registry type", "type", reg.Type, "error", err)
			os.Exit(1)
		}
	}

	// Load all persisted registry instances
	if err := regManager.LoadAll(ctx); err != nil {
		logger.Error("failed to load registry instances", "error", err)
		os.Exit(1)
	}

	// Seed the default registry catalogue on a clean install. Instances are created
	// disabled so an administrator can review them before they accept traffic.
	seedDefaultInstances(ctx, logger, cfg, regManager)

	// Optional opt-in demo data seeding for local UI preview (never runs by default)
	if cfg.SeedDemoPackages {
		seedDemoData(ctx, logger, regManager, authSvc)
	}

	// Global platform settings
	defaultSettings := admin.DefaultSettings(cfg.PublicBaseURL, cfg.EnableRootCompatRoute)
	defaultSettings.UpstreamProxyURL = cfg.UpstreamProxyURL
	defaultSettings.DefaultPubUpstreamURL = cfg.DefaultUpstreamURL
	defaultSettings.DefaultFlutterStorageURL = cfg.DefaultFlutterUpstreamURL
	if cfg.UpstreamDefaultTimeout > 0 {
		defaultSettings.UpstreamTimeoutSeconds = int(cfg.UpstreamDefaultTimeout.Seconds())
	}
	defaultSettings.Normalize()
	if existingSettings, err := settingsStore.GetSettings(ctx); err == nil && existingSettings != nil {
		existingSettings.Normalize()
		defaultSettings = existingSettings
	} else {
		_ = settingsStore.SaveSettings(ctx, defaultSettings)
	}
	if err := upstreamClient.ApplyConfig(defaultSettings.ToUpstreamConfig()); err != nil {
		logger.Warn("failed to apply persisted upstream settings", "error", err)
	}

	adminHandler := admin.NewHandler(
		regManager,
		authSvc,
		storageSvc,
		cacheCoord,
		metricsCol,
		auditSvc,
		settingsStore,
		defaultSettings,
	)
	adminHandler.SetUpstreamClient(upstreamClient)
	liveDefaults.Set(func(ctx context.Context, registryType string) (map[string]any, error) {
		st, err := adminHandler.GetSettings(ctx)
		if err != nil || st == nil {
			return nil, err
		}
		return st.BaseConfig(ctx, registryType)
	})

	srv := server.New(server.Options{
		Manager:               regManager,
		AdminHandler:          adminHandler,
		PublicBaseURLFunc:     func() string { return adminHandler.CurrentSettings().PublicBaseURL },
		EnableRootCompatRoute: func() bool { return adminHandler.CurrentSettings().EnableRootCompatRoute },
		AllowedOrigins:        cfg.AllowedOrigins,
	})

	// Start periodic shared background maintenance tasks
	runner := tasks.NewRunner(
		regManager,
		cacheCoord,
		metricsCol,
		cfg.CacheEvictionInterval,
		cfg.HealthCheckInterval,
		cfg.StatsFlushInterval,
		logger,
	)
	runner.Start(ctx)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		TLSConfig:         tlsConfig,
		Handler:           srv,
		ReadHeaderTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		logger.Info("shutting down depyard server...")
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutCancel()
		_ = metricsCol.Flush(shutCtx)
		_ = httpServer.Shutdown(shutCtx)
	}()

	logger.Info("depyard listening",
		"addr", cfg.ListenAddr, "tls", tlsConfig != nil,
		"installed_types", len(regManager.ListTypes()),
		"root_compat_route", defaultSettings.EnableRootCompatRoute,
	)
	if tlsConfig != nil {
		err = httpServer.ListenAndServeTLS("", "")
	} else {
		err = httpServer.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("server error", "error", err)
		os.Exit(1)
	}
}

// seedDefaultInstances adds one instance per installed ecosystem on the first
// install only. Every seeded instance is disabled until explicitly enabled in
// the admin console; existing installations are never modified.
func seedDefaultInstances(ctx context.Context, logger *slog.Logger, cfg config.Config, mgr *registry.Manager) {
	if !cfg.SeedDefaultRegistry {
		return
	}
	instances, err := mgr.ListInstances(ctx)
	if err != nil || len(instances) != 0 {
		return
	}

	const defaultSeedEnabled = false

	defInst, createErr := mgr.CreateInstance(ctx, &registry.Instance{
		Name:        cfg.DefaultRegistryName,
		Slug:        cfg.DefaultRegistrySlug,
		Type:        cfg.DefaultRegistryType,
		Description: "Default Dart & Flutter Pub mirror with hybrid local publishing and namespace protection.",
		Enabled:     defaultSeedEnabled,
		Mode:        registry.RepositoryMode(cfg.DefaultRegistryMode),
		UpstreamURL: cfg.DefaultUpstreamURL,
		Visibility:  registry.VisibilityPublic,
		AuthPolicy: registry.AuthPolicy{
			AllowAnonymousRead: true,
			RequireAuthPublish: true,
		},
		CachePolicy: registry.DefaultCachePolicy(),
		Config: map[string]any{
			"upstream_timeout_seconds": 30,
			"allow_retract":            true,
		},
		IsDefault: true,
	})
	if createErr != nil {
		logger.Warn("could not seed default registry instance", "error", createErr)
	} else {
		logger.Info("seeded default registry instance", "slug", defInst.Slug, "type", defInst.Type, "mode", defInst.Mode)
	}

	if cfg.SeedGoProxy {
		goInst, goErr := mgr.CreateInstance(ctx, &registry.Instance{
			Name:        cfg.DefaultGoName,
			Slug:        cfg.DefaultGoSlug,
			Type:        goproxy.RegistryType,
			Description: "Go module proxy (GOPROXY protocol) mirroring proxy.golang.org.",
			Enabled:     defaultSeedEnabled,
			Mode:        registry.ModeProxy,
			UpstreamURL: cfg.DefaultGoUpstreamURL,
			Visibility:  registry.VisibilityPublic,
			AuthPolicy: registry.AuthPolicy{
				AllowAnonymousRead: true,
				RequireAuthPublish: true,
			},
			CachePolicy: registry.DefaultCachePolicy(),
			Config: map[string]any{
				"upstream_timeout_seconds": 60,
				"include_prerelease":       true,
			},
			IsDefault: true,
		})
		if goErr != nil {
			logger.Warn("could not seed default go proxy instance", "error", goErr)
		} else {
			logger.Info("seeded default go proxy instance", "slug", goInst.Slug, "type", goInst.Type, "mode", goInst.Mode)
		}
	}

	if cfg.SeedPyPI {
		pyInst, pyErr := mgr.CreateInstance(ctx, &registry.Instance{
			Name:        cfg.DefaultPyPIName,
			Slug:        cfg.DefaultPyPISlug,
			Type:        pypi.RegistryType,
			Description: "Python Package Index mirror (PEP 503 Simple Repository API) with Twine-compatible publishing.",
			Enabled:     defaultSeedEnabled,
			Mode:        registry.RepositoryMode(cfg.DefaultPyPIMode),
			UpstreamURL: cfg.DefaultPyPIUpstreamURL,
			Visibility:  registry.VisibilityPublic,
			AuthPolicy: registry.AuthPolicy{
				AllowAnonymousRead: true,
				RequireAuthPublish: true,
			},
			CachePolicy: registry.DefaultCachePolicy(),
			Config: map[string]any{
				"upstream_timeout_seconds": 60,
				"include_prerelease":       false,
			},
			IsDefault: true,
		})
		if pyErr != nil {
			logger.Warn("could not seed default pypi instance", "error", pyErr)
		} else {
			logger.Info("seeded default pypi instance", "slug", pyInst.Slug, "type", pyInst.Type, "mode", pyInst.Mode)
		}
	}

	if cfg.SeedNPM {
		npmInst, npmErr := mgr.CreateInstance(ctx, &registry.Instance{
			Name:        cfg.DefaultNPMName,
			Slug:        cfg.DefaultNPMSlug,
			Type:        npm.RegistryType,
			Description: "npm Registry mirror (packuments and tarballs) with npm-compatible publishing.",
			Enabled:     defaultSeedEnabled,
			Mode:        registry.RepositoryMode(cfg.DefaultNPMMode),
			UpstreamURL: cfg.DefaultNPMUpstreamURL,
			Visibility:  registry.VisibilityPublic,
			AuthPolicy: registry.AuthPolicy{
				AllowAnonymousRead: true,
				RequireAuthPublish: true,
			},
			CachePolicy: registry.DefaultCachePolicy(),
			Config: map[string]any{
				"upstream_timeout_seconds": 60,
				"verify_integrity":         true,
			},
			IsDefault: true,
		})
		if npmErr != nil {
			logger.Warn("could not seed default npm instance", "error", npmErr)
		} else {
			logger.Info("seeded default npm instance", "slug", npmInst.Slug, "type", npmInst.Type, "mode", npmInst.Mode)
		}
	}

	if cfg.SeedFlutterStorage {
		flInst, flErr := mgr.CreateInstance(ctx, &registry.Instance{
			Name:        cfg.DefaultFlutterName,
			Slug:        cfg.DefaultFlutterSlug,
			Type:        flutter.RegistryType,
			Description: "Mirror for Flutter SDK releases, engine artifacts, and Gradle Maven storage (FLUTTER_STORAGE_BASE_URL).",
			Enabled:     defaultSeedEnabled,
			Mode:        registry.ModeProxy,
			UpstreamURL: cfg.DefaultFlutterUpstreamURL,
			Visibility:  registry.VisibilityPublic,
			AuthPolicy: registry.AuthPolicy{
				AllowAnonymousRead: true,
				RequireAuthPublish: true,
			},
			CachePolicy: registry.DefaultCachePolicy(),
			Config: map[string]any{
				"storage_name":             cfg.DefaultFlutterName,
				"allowed_prefixes":         flutter.DefaultAllowedPrefixes,
				"download_storage":         flutter.DefaultDownloadStorage(),
				"manifest_ttl_seconds":     300,
				"registry_ttl_seconds":     900,
				"rewrite_manifest_urls":    true,
				"serve_stale_on_error":     true,
				"upstream_timeout_seconds": 300,
			},
			IsDefault: true,
		})
		if flErr != nil {
			logger.Warn("could not seed default flutter storage instance", "error", flErr)
		} else {
			logger.Info("seeded default flutter storage instance", "slug", flInst.Slug, "type", flInst.Type, "mode", flInst.Mode)
		}
	}
	for _, reg := range objectFormats() {
		mode := registry.ModeHybrid
		if reg.DefaultUpstreamURL == "" {
			mode = registry.ModeHosted
		}
		_, err := mgr.CreateInstance(ctx, &registry.Instance{Name: reg.DisplayName, Slug: reg.Type, Type: reg.Type,
			Description: reg.Description, Mode: mode, UpstreamURL: reg.DefaultUpstreamURL, Enabled: defaultSeedEnabled,
			Visibility: registry.VisibilityPublic, AuthPolicy: registry.AuthPolicy{AllowAnonymousRead: true, RequireAuthPublish: true},
			CachePolicy: registry.DefaultCachePolicy(), IsDefault: reg.Type == "oci",
		})
		if err != nil {
			logger.Warn("could not seed registry instance", "type", reg.Type, "error", err)
		}
	}

}

func seedDemoData(ctx context.Context, logger *slog.Logger, manager *registry.Manager, authSvc *auth.Service) {
	// Also seed a second hosted private Pub instance so multi-registry management is immediately demonstrable.
	if _, err := manager.GetInstanceBySlug(ctx, "pub-private"); errors.Is(err, registry.ErrInstanceNotFound) {
		_, _ = manager.CreateInstance(ctx, &registry.Instance{
			Name:              "Pub Private Internal",
			Slug:              "pub-private",
			Type:              "pub",
			Description:       "Private hosted Dart/Flutter packages for internal mobile teams.",
			Enabled:           true,
			Mode:              registry.ModeHosted,
			Visibility:        registry.VisibilityPrivate,
			StorageQuotaBytes: 512 * 1024 * 1024,
			AuthPolicy: registry.AuthPolicy{
				AllowAnonymousRead: false,
				RequireAuthPublish: true,
			},
			CachePolicy: registry.DefaultCachePolicy(),
			Config: map[string]any{
				"allow_retract": true,
			},
		})
	}

	rt, ok := manager.GetDefaultRuntimeForType(pub.RegistryType)
	if !ok {
		return
	}
	pubReg, ok := rt.Registry.(*pub.PubRegistry)
	if !ok {
		return
	}

	adminUser, _ := authSvc.ListUsers(ctx)
	var principal *auth.Principal
	if len(adminUser) > 0 {
		principal = &auth.Principal{Authenticated: true, Kind: "user", User: adminUser[0]}
	} else {
		principal = &auth.Principal{Authenticated: true, Kind: "user", User: &auth.User{ID: "seed", Username: "admin", Email: "admin@localhost", Role: auth.RoleAdmin}}
	}

	samples := []struct {
		name        string
		versions    []string
		description string
		repo        string
		readme      string
		changelog   string
	}{
		{
			name:        "acme_design_kit",
			versions:    []string{"1.0.0", "1.1.0", "1.2.0"},
			description: "Composable Flutter UI primitives, adaptive theming, and accessible design tokens for multi-platform apps.",
			repo:        "https://github.com/acme/acme_design_kit",
			readme:      "# acme_design_kit\n\nA lightweight, composable Flutter design system hosted on your private Depyard.\n\n## Features\n\n- Zero external native dependencies\n- Light & dark semantic token palettes\n- Responsive layout primitives for mobile, desktop, and web\n\n## Quick Start\n\n```dart\nimport 'package:acme_design_kit/acme_design_kit.dart';\n\nfinal theme = AcmeTheme.light();\n```\n",
			changelog:   "## 1.2.0\n\n- Added adaptive navigation rail and high-contrast surface tokens.\n\n## 1.1.0\n\n- Improved keyboard focus rings across form inputs.\n\n## 1.0.0\n\n- Initial internal release.\n",
		},
		{
			name:        "acme_http_client",
			versions:    []string{"0.9.0", "1.0.0"},
			description: "Resilient Dart HTTP client with structured retries, bearer token interceptors, and OpenTelemetry tracing hooks.",
			repo:        "https://github.com/acme/acme_http_client",
			readme:      "# acme_http_client\n\nTyped HTTP client with exponential backoff, circuit breaking, and structured logging.\n\n```dart\nfinal client = AcmeHttpClient(baseUrl: Uri.parse('https://api.internal'));\nfinal res = await client.get('/v1/status');\n```\n",
			changelog:   "## 1.0.0\n\n- Stabilized retry interceptor API.\n\n## 0.9.0\n\n- Beta release for internal services.\n",
		},
		{
			name:        "acme_state_store",
			versions:    []string{"2.0.0-beta.1", "2.0.0"},
			description: "Deterministic reactive state container for Dart and Flutter with time-travel debugging and strict immutability.",
			repo:        "https://github.com/acme/acme_state_store",
			readme:      "# acme_state_store\n\nPredictable state management tailored for large Flutter monorepos.\n\n## Highlights\n\n- Strict compile-time state selectors\n- Built-in persistence middleware\n- Zero reflection or code generation required\n",
			changelog:   "## 2.0.0\n\n- Full Dart 3 pattern matching support.\n\n## 2.0.0-beta.1\n\n- Preview of immutable selector graph.\n",
		},
	}

	for _, sample := range samples {
		for _, ver := range sample.versions {
			tarBytes := makeSamplePubArchive(sample.name, ver, sample.description, sample.repo, sample.readme, sample.changelog)
			extracted, err := pub.ExtractPubArchive(bytes.NewReader(tarBytes))
			if err != nil {
				continue
			}
			_ = pubReg.PublishArchive(ctx, principal, extracted, bytes.NewReader(tarBytes))
		}
	}
	logger.Info("seeded demo hosted pub packages for local preview")
}

func makeSamplePubArchive(name, version, description, repo, readme, changelog string) []byte {
	var buf bytes.Buffer
	gzw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gzw)

	pubspec := "name: " + name + "\n" +
		"version: " + version + "\n" +
		"description: " + description + "\n" +
		"repository: " + repo + "\n" +
		"environment:\n  sdk: '>=3.2.0 <4.0.0'\n" +
		"dependencies:\n  meta: ^1.11.0\n"

	files := map[string]string{
		"pubspec.yaml":          pubspec,
		"README.md":             readme,
		"CHANGELOG.md":          changelog,
		"lib/" + name + ".dart": "library " + name + ";\nconst String packageVersion = '" + version + "';\n",
	}
	for fname, content := range files {
		data := []byte(content)
		_ = tw.WriteHeader(&tar.Header{
			Name: fname,
			Mode: 0o644,
			Size: int64(len(data)),
		})
		_, _ = tw.Write(data)
	}
	_ = tw.Close()
	_ = gzw.Close()
	return buf.Bytes()
}
