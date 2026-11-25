// https://nuxt.com/docs/api/configuration/nuxt-config
const backendTarget =
  (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env
    ?.NUXT_BACKEND_URL || 'http://127.0.0.1:8080'

export default defineNuxtConfig({
  compatibilityDate: '2025-01-01',
  devtools: { enabled: false },
  ssr: false,
  modules: ['@nuxtjs/tailwindcss'],
  css: ['~/assets/css/tailwind.css'],

  app: {
    head: {
      title: 'Mirror Registry',
      meta: [
        { name: 'viewport', content: 'width=device-width, initial-scale=1' },
        { name: 'description', content: 'Self-hosted multi-format package and artifact registry platform' },
      ],
    },
  },

  nitro: {
    routeRules: {
      '/api/**': { proxy: `${backendTarget}/api/**` },
      '/r/**': { proxy: `${backendTarget}/r/**` },
      '/packages/**': { proxy: `${backendTarget}/packages/**` },
      '/flutter_infra_release/**': { proxy: `${backendTarget}/flutter_infra_release/**` },
      '/flutter_infra/**': { proxy: `${backendTarget}/flutter_infra/**` },
      '/download.flutter.io/**': { proxy: `${backendTarget}/download.flutter.io/**` },
      '/dart-archive/**': { proxy: `${backendTarget}/dart-archive/**` },
      '/healthz': { proxy: `${backendTarget}/healthz` },
    } as Record<string, any>,
  },

  vite: {
    server: {
      allowedHosts: true,
    },
  },
})
