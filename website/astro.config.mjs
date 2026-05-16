import { defineConfig } from 'astro/config';
import tailwind from '@astrojs/tailwind';
import sitemap from '@astrojs/sitemap';

// Wenn du eigene Domain anbindest: hier eintragen.
// Wird fuer Sitemap und Open Graph Tags verwendet.
const SITE = process.env.PUBLIC_SITE_URL || 'https://streborn.app';

export default defineConfig({
  site: SITE,
  output: 'static',
  // directory Format heisst: Astro baut /impressum/index.html statt /impressum.html.
  // Damit funktionieren Pretty URLs auf jedem Webserver auch ohne Apache Rewrites.
  trailingSlash: 'always',
  build: {
    format: 'directory',
    // hash basierte Dateinamen damit Browser Cache zuverlaessig invalidiert
    assets: '_assets',
  },
  integrations: [
    tailwind({
      applyBaseStyles: true,
    }),
    sitemap({
      i18n: {
        defaultLocale: 'en',
        locales: {
          en: 'en',
          de: 'de',
        },
      },
    }),
  ],
  i18n: {
    defaultLocale: 'en',
    locales: ['en', 'de'],
    routing: {
      prefixDefaultLocale: false,
    },
  },
  // Erlaubt nur die in der CSP gewuenschten externen Quellen.
  // Achtung: harte CSP wird ueber Web Server Header gesetzt, dies ist nur Build Hint.
  vite: {
    build: {
      sourcemap: false,
      cssMinify: 'lightningcss',
    },
  },
});
